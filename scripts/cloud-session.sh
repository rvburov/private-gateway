#!/usr/bin/env bash
# SessionStart hook of Claude Code cloud sessions (claude.ai/code, the Claude app, claude --cloud).
# The cloud machine brings its own git identity, while AGENTS.md wants the owner as author and
# committer of every commit. .claude/settings.json calls it only when CLAUDE_CODE_REMOTE is true.
set -euo pipefail
[[ ${CLAUDE_CODE_REMOTE:-} == true ]] || exit 0
cd "$(dirname "${BASH_SOURCE[0]}")/.."
git config user.name 'Roman Burov'
git config user.email 'romanburov89@gmail.com'
# Whatever this prints becomes context of the session.
for name in GIT_AUTHOR_NAME GIT_AUTHOR_EMAIL GIT_COMMITTER_NAME GIT_COMMITTER_EMAIL; do
  if [[ -n ${!name:-} ]]; then
    printf '%s is set in this session and overrides git config: commit with env -u %s git commit.\n' "$name" "$name"
  fi
done
printf 'Cloud copy of the repository: git commits are authored by Roman Burov. AGENTS.md says how a cloud copy reaches master.\n'
