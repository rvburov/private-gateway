#!/usr/bin/env bash
# Prepare a new VPS for CI/CD and add it to deploy/servers. Run it in the main project folder on
# your PC (Git Bash on Windows) with SSH access to the new server as a user who may use sudo:
#
#   bash scripts/add-server.sh NAME SERVER_IPV4 PANEL_DOMAIN SSH_USER [SSH_PORT]
#
# The server needs Ubuntu 26.04 amd64, a DNS A record of PANEL_DOMAIN pointing to it and open TCP
# 22, 80 and 443. The script shows the server's SSH host key for you to compare with the provider's
# console, uploads the restricted deployment receiver from the current commit over SSH (sudo asks
# for your password on the server) and commits deploy/servers/NAME.conf to master. The next push
# to master makes CI/CD install the program there; later pushes update it with every other server.
# Nothing is changed on the server before you confirm its host key.
set -euo pipefail
cd "$(dirname "$0")/.."

fail() { printf 'Server not added: %s\n' "$*" >&2; exit 1; }
[[ $# == 4 || $# == 5 ]] || fail 'usage: bash scripts/add-server.sh NAME SERVER_IPV4 PANEL_DOMAIN SSH_USER [SSH_PORT]'
name=$1 host=$2 domain=$3 ssh_user=$4 port=${5:-22}
[[ $ssh_user =~ ^[a-z_][a-z0-9_-]{0,31}$ ]] || fail 'invalid SSH user name'
for tool in git ssh ssh-keyscan ssh-keygen scp python3 sha256sum; do
  command -v "$tool" > /dev/null || fail "required command is missing: $tool"
done

# The record lands on master, so start from the newest master in a clean main folder.
[[ $(git branch --show-current) == master ]] || fail 'run it in the main project folder, which is on master'
[[ -z $(git status --porcelain) ]] || fail 'the main folder has uncommitted changes; sort them out first'
git fetch --quiet origin master || fail 'could not reach GitHub'
if ! git merge-base --is-ancestor origin/master master; then
  git merge-base --is-ancestor master origin/master || fail 'local master and origin/master have diverged'
  git merge --quiet --ff-only origin/master
fi
python3 scripts/servers.py check "$name" --host "$host" --port "$port" --domain "$domain" \
  || fail 'fix the values above; nothing was changed'
# Caddy obtains the panel certificate for this name, so DNS must already point to the server.
resolved=$(python3 -c 'import socket, sys; print(socket.gethostbyname(sys.argv[1]))' "$domain" 2> /dev/null || true)
[[ $resolved == "$host" ]] || fail "$domain resolves to ${resolved:-nothing}, not $host; create its DNS A record first"

scanned=$(ssh-keyscan -T 15 -t ed25519 -p "$port" "$host" 2> /dev/null \
  | awk '$2 == "ssh-ed25519" { print $2, $3; exit }')
[[ -n $scanned ]] || fail "could not read the ED25519 SSH host key of $host:$port"
fingerprint=$(printf '%s\n' "$scanned" | ssh-keygen -lf - | awk '{ print $2 }')
cat << EOF
New server $name: SSH $host:$port, panel https://$domain/
SSH host key fingerprint: $fingerprint (ED25519)
Compare it with the fingerprint your VPS provider shows, or with the output of
  sudo ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub
in the provider's web console of the server.
EOF
read -r -p 'Type yes if the fingerprints match: ' answer
[[ $answer == yes ]] || fail 'the host key was not confirmed; nothing was changed'

# Every SSH call trusts exactly the confirmed key and leaves ~/.ssh/known_hosts alone.
work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
if [[ $port == 22 ]]; then known_name=$host; else known_name="[$host]:$port"; fi
printf '%s %s\n' "$known_name" "$scanned" > "$work/known_hosts"
ssh_options=(-o StrictHostKeyChecking=yes -o "UserKnownHostsFile=$work/known_hosts" -o LogLevel=ERROR)
destination="$ssh_user@$host"

# The bootstrap bundle comes from the current commit, the same files CI verifies.
commit=$(git rev-parse HEAD)
bundle="$work/bundle"
mkdir "$bundle"
git show HEAD:deploy/docker/bootstrap.sh > "$bundle/bootstrap.sh"
git show HEAD:deploy/docker/bootstrap-receiver.sh > "$bundle/bootstrap-receiver.sh"
git show HEAD:scripts/verify-release.py > "$bundle/verify-release.py"
git show HEAD:deploy/servers/deploy-key.pub > "$bundle/deploy-key.pub"
printf '%s\n' "$commit" > "$bundle/commit.sha"
(cd "$bundle" && sha256sum bootstrap.sh bootstrap-receiver.sh verify-release.py commit.sha > SHA256SUMS)

remote=$(ssh "${ssh_options[@]}" -p "$port" "$destination" 'mktemp -d /tmp/private-gateway-bootstrap.XXXXXXXX')
[[ $remote =~ ^/tmp/private-gateway-bootstrap\.[A-Za-z0-9]{8}$ ]] || fail 'the server returned an unexpected temporary folder'
scp -q "${ssh_options[@]}" -P "$port" "$bundle"/* "$destination:$remote/"
echo 'Preparing the deployment receiver; sudo on the server may ask for your password.'
ssh -t "${ssh_options[@]}" -p "$port" "$destination" \
  "sudo bash $remote/bootstrap.sh --ref $commit --host $host --domain $domain --deploy-key $remote/deploy-key.pub;
   status=\$?; rm -rf -- $remote; exit \$status" \
  || fail 'the server bootstrap stopped; its message is above'

record=$(python3 scripts/servers.py add "$name" --host "$host" --port "$port" --domain "$domain" --host-key "$scanned")
git add "$record"
git commit --quiet -m "Add server $name" -m "Panel https://$domain/ on $host:$port, prepared by scripts/add-server.sh."
printf '\nServer %s is prepared and %s is committed to master.\n' "$name" "$record"
printf 'Not on GitHub yet:\n'
git log --format='  %h %s' origin/master..master
read -r -p 'Push master now? CI/CD then installs the program on the new server and updates the others (yes/no): ' answer
if [[ $answer == yes ]]; then
  git push origin master
  printf '\nPushed. In GitHub Actions wait for a green "Deploy Docker to VPS (%s)".\n' "$name"
else
  printf '\nNot pushed. When ready: git push origin master\n'
fi
cat << EOF
After the first deployment, create the administrator over SSH on the new server:
  ssh -p $port $destination
  sudo python3 /opt/private-gateway-docker/current/docker/admin-credentials.py
Then open https://$domain/, sign in and connect the authenticator app.
EOF
