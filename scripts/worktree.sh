#!/usr/bin/env bash
#
# Рабочие папки агентов (git worktree): создать, проверить, обновить, влить в master, убрать.
# Правила — в AGENTS.md, раздел «Параллельная работа агентов».
#
#   scripts/worktree.sh                 # то же, что check
#   scripts/worktree.sh check           # все папки и ветки: незакоммиченное, не влитое, не отправленное
#   scripts/worktree.sh new <имя>       # (другие ИИ) новая папка .agents/worktrees/<имя>
#                                       #   на ветке <имя> от свежего master
#   scripts/worktree.sh sync            # (в рабочей папке) поставить её ветку на свежий master
#   scripts/worktree.sh merge [<имя>]   # перемотать master на ветку; без имени — на ветку этой папки
#   scripts/worktree.sh remove <имя>    # убрать папку и её ветку, только когда всё уже в master
#
# <имя> в merge и remove — папка или ветка. Claude Code называет свои ветки worktree-<имя>.
#
# Папки лежат внутри основной, в каталогах с точкой, которые исключает .gitignore:
#   .claude/worktrees/<имя>   - место Claude Code (EnterWorktree, claude --worktree);
#   .agents/worktrees/<имя>   - другие ИИ; эти папки создаёт этот скрипт.
# Пути отсчитываются от основной папки, поэтому правило одно на любой машине и ОС. В любом другом
# месте проекта рабочая папка видна как неотслеживаемые файлы основной папки и попадает в контекст
# проверок Docker.
#
# «Свежий master» — локальный master или origin/master, смотря какой из них уже содержит другой:
# облачные копии и VPS отправляют работу в GitHub, а вливания здесь остаются локальными до push.
# Если они разошлись, скрипт останавливается, и решает пользователь.
#
# Ни одна команда не удаляет незакоммиченную работу или коммиты, которых нет в master: скрипт
# называет такие случаи и останавливается. push сюда не входит: каждый push в master выкладывается
# на серверы, поэтому он делается только по просьбе пользователя. На Windows запускать из Git Bash.
set -euo pipefail

BASE=master
REMOTE=origin
MAX_NAME=20
LONGEST_INSIDE=120   # самый длинный путь внутри копии (исходники, локальные сборки), с запасом

die()  { echo "Error: $*" >&2; exit 1; }
note() { echo "  $*"; }

git rev-parse --git-dir > /dev/null 2>&1 || die "run inside the repository"

# Пути в том виде, как их пишет git («C:/...» на Windows), а не bash («/c/...»), — чтобы их можно было сравнивать.
MAIN=$(git worktree list --porcelain | sed -n '1s/^worktree //p')
CLAUDE_ROOT="$MAIN/.claude/worktrees"
AGENTS_ROOT="$MAIN/.agents/worktrees"
HERE=$(git rev-parse --show-toplevel)

# Список папок: путь, ветка, флаги (locked, prunable) через разделитель US (\037). Табуляция не
# годится: read считает её пробелом, и пустая ветка у папки без ветки бы схлопнулась.
SEP=$'\037'
worktrees() {
  git worktree list --porcelain | awk '
    /^worktree / { if (p != "") print p "\037" br "\037" fl; p = substr($0, 10); br = ""; fl = "" }
    /^branch /   { br = substr($0, 19) }
    /^locked/    { fl = fl "locked," }
    /^prunable/  { fl = fl "prunable," }
    END          { if (p != "") print p "\037" br "\037" fl }'
}

# Путь папки, где выписана ветка (пусто — нигде).
path_of_branch() {
  worktrees | awk -F '\037' -v b="$1" '$2 == b { print $1 }'
}

# Папка по имени ветки или по имени папки: «путь SEP ветка SEP флаги».
resolve() {
  local hit
  hit=$(worktrees | awk -F '\037' -v n="$1" '$2 == n')
  if [[ -z "$hit" ]]; then
    hit=$(worktrees | awk -F '\037' -v n="$1" '{ k = split($1, s, "/") } s[k] == n')
  fi
  if [[ $(printf '%s\n' "$hit" | awk 'NF { c++ } END { print c + 0 }') -gt 1 ]]; then
    die "several folders match $1 - name the branch"
  fi
  printf '%s' "$hit"
}

# Чьё это место: claude, agents, inside-plain, inside-dot, outside.
place_of() {
  case "$1/" in
    "$CLAUDE_ROOT/"*) echo claude ;;
    "$AGENTS_ROOT/"*) echo agents ;;
    "$MAIN/".*)       echo inside-dot ;;
    "$MAIN/"*)        echo inside-plain ;;
    *)                echo outside ;;
  esac
}

# Число изменений в папке: tracked — правки отслеживаемых файлов, all — ещё и новые файлы.
changes() {
  if [[ "$2" == tracked ]]; then
    git -C "$1" status --porcelain --untracked-files=no | wc -l | tr -d ' '
  else
    git -C "$1" status --porcelain | wc -l | tr -d ' '
  fi
}

ahead()  { git rev-list --count "$BASE..$1"; }   # коммиты ветки, которых нет в master
behind() { git rev-list --count "$1..$BASE"; }   # коммиты master, которых нет в ветке

# Первые строки вывода с отступом; sed дочитывает поток до конца, поэтому git не получает SIGPIPE.
first() { sed -n "1,${1}s/^/      /p"; }

# Тихо обновить origin/master; без сети используется последнее полученное состояние.
fetch_origin() {
  if ! git fetch --quiet "$REMOTE" "$BASE" 2> /dev/null; then
    note "could not reach $REMOTE; using the last fetched $REMOTE/$BASE"
  fi
}

# Локальный master или origin/master — смотря какой из них уже содержит другой.
newest_base() {
  local remote="$REMOTE/$BASE"
  if ! git rev-parse -q --verify "$remote" > /dev/null; then
    echo "$BASE"
  elif git merge-base --is-ancestor "$remote" "$BASE"; then
    echo "$BASE"
  elif git merge-base --is-ancestor "$BASE" "$remote"; then
    echo "$remote"
  else
    die "local $BASE and $remote have diverged - the user decides how to join them"
  fi
}

cmd_check() {
  local problems=0 path branch flags dirty a b
  echo "Main folder: $MAIN"
  echo "Worktrees:   Claude Code - $CLAUDE_ROOT/<name>"
  echo "             other AIs   - $AGENTS_ROOT/<name>"
  echo

  # Основная папка стоит на master и чиста: агенты в ней не работают.
  branch=$(git -C "$MAIN" branch --show-current)
  dirty=$(changes "$MAIN" all)
  echo "[main] branch ${branch:-(detached)}"
  if [[ "$branch" != "$BASE" ]]; then
    note "WARNING: the main folder is not on $BASE"
    problems=$((problems + 1))
  fi
  if [[ "$dirty" != 0 ]]; then
    note "WARNING: uncommitted changes in the main folder: $dirty"
    git -C "$MAIN" status --short | first 20
    problems=$((problems + 1))
  fi
  fetch_origin
  if git rev-parse -q --verify "$REMOTE/$BASE" > /dev/null; then
    a=$(git rev-list --count "$REMOTE/$BASE..$BASE")
    b=$(git rev-list --count "$BASE..$REMOTE/$BASE")
    if [[ "$a" != 0 ]]; then
      note "not pushed to GitHub: $a commit(s) of $BASE (push only on request: it deploys to the servers)"
    fi
    if [[ "$b" != 0 ]]; then
      note "$REMOTE/$BASE is ahead by $b commit(s) from another copy; sync in a worktree takes them in"
    fi
  fi

  # Рабочие папки, где бы они ни были.
  while IFS="$SEP" read -r path branch flags; do
    [[ "$path" != "$MAIN" ]] || continue
    echo
    echo "[folder] $path"
    echo "  branch ${branch:-(detached)}"
    if [[ "$flags" == *prunable* ]]; then
      note "WARNING: the folder is gone from disk but git still lists it (git worktree prune drops the record)"
      problems=$((problems + 1))
      continue
    fi
    case "$(place_of "$path")" in
      claude)  note "place: Claude Code" ;;
      agents)  note "place: other AIs" ;;
      outside) note "place: outside the project - the agent program created it itself; the same rules apply" ;;
      inside-plain)
        note "WARNING: inside the main folder outside the ignored places - git status of the main folder"
        note "and the Docker checks context pick it up; move it to .claude/worktrees or .agents/worktrees"
        problems=$((problems + 1)) ;;
      inside-dot)
        note "WARNING: inside the main folder but not in its place - it belongs in .claude/worktrees"
        note "or .agents/worktrees"
        problems=$((problems + 1)) ;;
    esac
    if [[ "$flags" == *locked* ]]; then
      note "locked (git worktree lock) - an agent program is working in it now"
    fi
    dirty=$(changes "$path" all)
    if [[ "$dirty" != 0 ]]; then
      note "UNCOMMITTED changes: $dirty"
      git -C "$path" status --short | first 10
      problems=$((problems + 1))
    fi
    if [[ -n "$branch" ]]; then
      a=$(ahead "$branch")
      b=$(behind "$branch")
      if [[ "$a" != 0 ]]; then
        note "NOT IN $BASE: $a commit(s)"
        git log -n 10 --format='%h %s' "$BASE..$branch" | first 10
        problems=$((problems + 1))
      fi
      if [[ "$b" != 0 ]]; then
        note "$BASE moved ahead by $b - sync before merging"
      fi
      if [[ "$a" == 0 && "$dirty" == 0 ]]; then
        note "everything is in $BASE - the folder can go (remove ${path##*/}) if the user agrees"
      fi
    fi
  done < <(worktrees)

  # Ветки без папки: о их работе легко забыть.
  while read -r branch; do
    if [[ "$branch" == "$BASE" || -n "$(path_of_branch "$branch")" ]]; then continue; fi
    a=$(ahead "$branch")
    [[ "$a" != 0 ]] || continue
    echo
    echo "[branch without folder] $branch"
    note "NOT IN $BASE: $a commit(s)"
    git log -n 10 --format='%h %s' "$BASE..$branch" | first 10
    problems=$((problems + 1))
  done < <(git for-each-ref refs/heads --format='%(refname:short)')

  echo
  if [[ "$problems" == 0 ]]; then
    echo "RESULT: everything is in $BASE, nothing uncommitted."
  else
    echo "RESULT: $problems place(s) need attention - show them to the user."
  fi
}

cmd_new() {
  local name="${1:-}" dest start
  [[ -n "$name" ]] || die "name it: scripts/worktree.sh new <name>"
  if [[ ! "$name" =~ ^[a-z0-9][a-z0-9-]*$ || ${#name} -gt $MAX_NAME ]]; then
    die "the name is lowercase Latin letters, digits and dashes, at most $MAX_NAME characters"
  fi
  if git show-ref -q --verify "refs/heads/$name"; then
    die "branch $name already exists - pick another name or remove it"
  fi
  dest="$AGENTS_ROOT/$name"
  [[ ! -e "$dest" ]] || die "folder $dest already exists"
  if (( ${#dest} + 1 + LONGEST_INSIDE > 259 )); then
    echo "Warning: $dest is long - Windows without long paths may hit the 260-character limit." >&2
  fi

  fetch_origin
  start=$(newest_base)
  mkdir -p "$AGENTS_ROOT"
  git worktree add -q --no-track -b "$name" "$dest" "$start"
  echo "Created worktree: $dest"
  echo "Branch: $name (from $start, $(git rev-parse --short "$start"))"
  echo "Work only there from now on. (Claude Code creates its own folder: EnterWorktree with a name.)"
}

cmd_sync() {
  local branch base
  [[ "$HERE" != "$MAIN" ]] || die "sync runs in a worktree, not in the main folder"
  branch=$(git branch --show-current)
  [[ -n "$branch" ]] || die "this folder is not on a branch"
  [[ "$(changes "$HERE" tracked)" == 0 ]] || die "uncommitted edits - commit first"
  fetch_origin
  base=$(newest_base)
  if ! git rebase -q "$base"; then
    echo "Conflict while moving onto $base. Resolve it and run git rebase --continue, or cancel: git rebase --abort." >&2
    exit 1
  fi
  echo "Branch $branch sits on the newest $BASE ($base, $(git rev-parse --short "$base"))."
  echo "Next: bash scripts/dev.sh test, then bash scripts/worktree.sh merge"
}

cmd_merge() {
  local name="${1:-}" hit path branch a base
  if [[ -z "$name" ]]; then
    [[ "$HERE" != "$MAIN" ]] || die "in the main folder name it: scripts/worktree.sh merge <name>"
    path="$HERE"
    branch=$(git branch --show-current)
  else
    hit=$(resolve "$name")
    if [[ -n "$hit" ]]; then
      IFS="$SEP" read -r path branch _ <<< "$hit"
    elif git show-ref -q --verify "refs/heads/$name"; then
      path=""; branch="$name"   # ветка без папки
    else
      die "no folder or branch named $name"
    fi
  fi
  [[ -n "$branch" && "$branch" != "$BASE" ]] || die "nothing to merge: the folder is not on a branch or it is $BASE"

  if [[ -n "$path" && "$(changes "$path" tracked)" != 0 ]]; then
    die "uncommitted edits in $path - commit first"
  fi
  a=$(ahead "$branch")
  if [[ "$a" == 0 ]]; then
    echo "Branch $branch has no commits outside $BASE - nothing to merge."
    return 0
  fi
  fetch_origin
  base=$(newest_base)
  git merge-base --is-ancestor "$base" "$branch" \
    || die "$base moved ahead - scripts/worktree.sh sync in the branch folder, test, then merge"
  [[ "$(git -C "$MAIN" branch --show-current)" == "$BASE" ]] || die "the main folder is not on $BASE"
  [[ "$(changes "$MAIN" tracked)" == 0 ]] \
    || die "uncommitted edits in the main folder - sort them out with the user"

  git -C "$MAIN" merge -q --ff-only "$branch"
  echo "Merged $a commit(s) into $BASE (branch $branch)."
  echo "Not pushed: push only on the user's request - every push to $BASE deploys to the servers."
  echo "The folder and branch stay; scripts/worktree.sh remove clears them once the user agrees."
}

cmd_remove() {
  local name="${1:-}" hit path branch flags all
  [[ -n "$name" ]] || die "name it: scripts/worktree.sh remove <name>"
  [[ "$name" != "$BASE" ]] || die "$BASE is never removed"
  hit=$(resolve "$name")
  [[ -n "$hit" ]] || die "no worktree with folder or branch $name (git worktree list)"
  IFS="$SEP" read -r path branch flags <<< "$hit"
  [[ "$path" != "$MAIN" ]] || die "this is the main folder"
  [[ "$HERE" != "$path" ]] || die "you are inside $path - run it from another folder"
  if [[ "$flags" == *locked* ]]; then
    die "$path is locked - an agent program works in it; close that chat first"
  fi

  all=$(changes "$path" all)
  if [[ "$all" != 0 ]]; then
    echo "$path has uncommitted changes ($all) - left untouched:" >&2
    git -C "$path" status --short | first 20 >&2
    exit 1
  fi
  if [[ -n "$branch" && "$(ahead "$branch")" != 0 ]]; then
    echo "Branch $branch has commits that $BASE lacks - left untouched:" >&2
    git log --format='%h %s' "$BASE..$branch" | first 20 >&2
    exit 1
  fi

  git worktree remove "$path"
  if [[ -n "$branch" ]]; then
    git branch -q -D "$branch"   # все её коммиты в master — проверено выше
  fi
  echo "Removed folder $path${branch:+ and branch $branch}."
}

case "${1:-check}" in
  check)  cmd_check ;;
  new)    cmd_new "${2:-}" ;;
  sync)   cmd_sync ;;
  merge)  cmd_merge "${2:-}" ;;
  remove) cmd_remove "${2:-}" ;;
  *)      die "unknown command '$1'; available: check, new, sync, merge, remove" ;;
esac
