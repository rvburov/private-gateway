#!/usr/bin/env bash
# Хук SessionStart облачных сессий Claude Code (claude.ai/code, приложение Claude, claude --cloud).
# Облачная машина приходит со своим автором git, а AGENTS.md требует, чтобы автором и коммитером
# каждого коммита был владелец. .claude/settings.json вызывает его, только если CLAUDE_CODE_REMOTE
# равен true.
set -euo pipefail
[[ ${CLAUDE_CODE_REMOTE:-} == true ]] || exit 0
cd "$(dirname "${BASH_SOURCE[0]}")/.."
git config user.name 'Roman Burov'
git config user.email 'romanburov89@gmail.com'
# Всё, что печатается здесь, попадает в контекст сессии.
for name in GIT_AUTHOR_NAME GIT_AUTHOR_EMAIL GIT_COMMITTER_NAME GIT_COMMITTER_EMAIL; do
  if [[ -n ${!name:-} ]]; then
    printf '%s is set in this session and overrides git config: commit with env -u %s git commit.\n' "$name" "$name"
  fi
done
printf 'Cloud copy of the repository: git commits are authored by Roman Burov. AGENTS.md says how a cloud copy reaches master.\n'
