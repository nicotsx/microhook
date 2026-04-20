#!/bin/sh

set -eu

if [ "$#" -ne 1 ]; then
  echo "usage: $0 <release-tarball>" >&2
  exit 2
fi

if [ "$(uname -s)" != "Linux" ]; then
  echo "install smoke test skipped: Linux only" >&2
  exit 0
fi

if ! command -v curl >/dev/null 2>&1; then
  echo "install smoke test requires curl" >&2
  exit 1
fi

artifact=$1
tmpdir=$(mktemp -d)
port=${PORT:-19464}
service_pid=

cleanup() {
  if [ -n "${service_pid}" ] && kill -0 "${service_pid}" >/dev/null 2>&1; then
    kill "${service_pid}" >/dev/null 2>&1 || true
    wait "${service_pid}" >/dev/null 2>&1 || true
  fi
  rm -rf "${tmpdir}"
}

trap cleanup EXIT INT TERM

tar -xzf "${artifact}" -C "${tmpdir}"

package_name=$(basename "${artifact}")
package_name=${package_name%.tar.gz}
package_dir="${tmpdir}/${package_name}"
binary="${package_dir}/microhook"

[ -x "${binary}" ]
[ -f "${package_dir}/README.md" ]
[ -f "${package_dir}/docs/install.md" ]
[ -f "${package_dir}/examples/config.yml" ]
[ -f "${package_dir}/systemd/microhook.service" ]

token=$("${binary}" generate-token)
config_path="${tmpdir}/config.yml"
bad_config_path="${tmpdir}/bad-config.yml"
db_path="${tmpdir}/microhook.db"
log_path="${tmpdir}/microhook.log"

cat > "${config_path}" <<EOF
server:
  listen: "127.0.0.1:${port}"
  log_format: "json"

auth:
  tokens:
    - name: "smoke"
      value: "${token}"
      actions: ["hello"]

storage:
  path: "${db_path}"
  retention_days: 7
  max_runs: 100

actions:
  - name: "hello"
    description: "Linux install smoke test"
    command: ["/bin/sh", "-c", "cat >/dev/null; printf ready"]
    timeout: "5s"
    concurrency_policy: "allow"
    max_output_bytes: 1024
    enabled: true
EOF

cat > "${bad_config_path}" <<EOF
storage:
  retention_days: 1
EOF

"${binary}" validate-config -config "${config_path}" >/dev/null

if "${binary}" validate-config -config "${bad_config_path}" >/dev/null 2>&1; then
  echo "expected invalid config validation to fail" >&2
  exit 1
fi

"${binary}" serve -config "${config_path}" >"${log_path}" 2>&1 &
service_pid=$!

attempts=50
while [ "${attempts}" -gt 0 ]; do
  if curl -fsS "http://127.0.0.1:${port}/healthz" >/dev/null 2>&1; then
    break
  fi
  attempts=$((attempts - 1))
  sleep 0.1
done

if [ "${attempts}" -eq 0 ]; then
  cat "${log_path}" >&2
  echo "service did not become healthy" >&2
  exit 1
fi

status=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:${port}/v1/runs")
if [ "${status}" != "401" ]; then
  echo "expected unauthenticated run list to return 401, got ${status}" >&2
  exit 1
fi

invoke_response=$(curl -fsS \
  -H "Authorization: Bearer ${token}" \
  -H 'X-Request-Id: smoke-header' \
  -H 'Content-Type: application/json' \
  -d '{"mode":"sync","input":{"request_id":"body-request-id","reason":"smoke"}}' \
  "http://127.0.0.1:${port}/v1/actions/hello/runs")

printf '%s' "${invoke_response}" | grep '"status":"succeeded"' >/dev/null
printf '%s' "${invoke_response}" | grep '"stdout_tail":"ready"' >/dev/null
printf '%s' "${invoke_response}" | grep '"request_id":"smoke-header"' >/dev/null

run_id=$(printf '%s' "${invoke_response}" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
if [ -z "${run_id}" ]; then
  echo "expected invoke response to contain a run id" >&2
  exit 1
fi

run_response=$(curl -fsS -H "Authorization: Bearer ${token}" "http://127.0.0.1:${port}/v1/runs/${run_id}")
printf '%s' "${run_response}" | grep '"id":"'"${run_id}"'"' >/dev/null

list_response=$(curl -fsS -H "Authorization: Bearer ${token}" "http://127.0.0.1:${port}/v1/runs?action=hello&status=succeeded")
printf '%s' "${list_response}" | grep '"action":"hello"' >/dev/null

echo "install smoke test passed for ${artifact}"
