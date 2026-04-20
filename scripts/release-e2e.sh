#!/bin/sh

set -eu

if [ "$#" -ne 1 ]; then
  echo "usage: $0 <release-tarball>" >&2
  exit 2
fi

if [ "$(uname -s)" != "Linux" ]; then
  echo "release e2e skipped: Linux only" >&2
  exit 0
fi

for required in curl grep sed tar mktemp; do
  if ! command -v "${required}" >/dev/null 2>&1; then
    echo "release e2e requires ${required}" >&2
    exit 1
  fi
done

artifact=$1
tmpdir=$(mktemp -d)
port=${PORT:-19465}
base_url="http://127.0.0.1:${port}"
service_pid=

fail() {
  echo "$1" >&2
  exit 1
}

cleanup() {
  if [ -n "${service_pid}" ] && kill -0 "${service_pid}" >/dev/null 2>&1; then
    kill "${service_pid}" >/dev/null 2>&1 || true
    wait "${service_pid}" >/dev/null 2>&1 || true
  fi
  rm -rf "${tmpdir}"
}

assert_status() {
  actual=$1
  expected=$2
  label=$3

  if [ "${actual}" != "${expected}" ]; then
    fail "expected ${label} status ${expected}, got ${actual}"
  fi
}

assert_contains() {
  file_path=$1
  expected=$2
  label=$3

  if ! grep -F "${expected}" "${file_path}" >/dev/null 2>&1; then
    cat "${file_path}" >&2
    fail "expected ${label} to contain ${expected}"
  fi
}

extract_json_string() {
  key=$1
  file_path=$2
  value=$(sed -n 's/.*"'"${key}"'":"\([^"]*\)".*/\1/p' "${file_path}" | sed -n '1p')

  if [ -z "${value}" ]; then
    cat "${file_path}" >&2
    fail "expected ${file_path} to contain JSON string field ${key}"
  fi

  printf '%s' "${value}"
}

request_status() {
  output_path=$1
  shift
  curl -sS -o "${output_path}" -w '%{http_code}' "$@"
}

wait_for_health() {
  attempts=50

  while [ "${attempts}" -gt 0 ]; do
    if curl -fsS "${base_url}/healthz" >/dev/null 2>&1; then
      return 0
    fi

    attempts=$((attempts - 1))
    sleep 0.1
  done

  cat "${log_path}" >&2
  fail "service did not become healthy"
}

start_service() {
  "${binary}" serve -config "${config_path}" >"${log_path}" 2>&1 &
  service_pid=$!
  wait_for_health
}

restart_service() {
  if [ -z "${service_pid}" ] || ! kill -0 "${service_pid}" >/dev/null 2>&1; then
    fail "expected service process to be running before restart"
  fi

  kill -KILL "${service_pid}" >/dev/null 2>&1 || true
  wait "${service_pid}" >/dev/null 2>&1 || true
  service_pid=
  start_service
}

wait_for_run_status() {
  run_id=$1
  token=$2
  expected_status=$3
  output_path=$4
  attempts=100

  while [ "${attempts}" -gt 0 ]; do
    if status=$(request_status "${output_path}" -H "Authorization: Bearer ${token}" "${base_url}/v1/runs/${run_id}" 2>/dev/null); then
      if [ "${status}" = "200" ] && grep -F '"status":"'"${expected_status}"'"' "${output_path}" >/dev/null 2>&1; then
        return 0
      fi
    fi

    attempts=$((attempts - 1))
    sleep 0.1
  done

  if [ -f "${output_path}" ]; then
    cat "${output_path}" >&2
  fi
  cat "${log_path}" >&2
  fail "run ${run_id} did not reach status ${expected_status}"
}

trap cleanup EXIT INT TERM

tar -xzf "${artifact}" -C "${tmpdir}"

package_name=$(basename "${artifact}")
package_name=${package_name%.tar.gz}
package_dir="${tmpdir}/${package_name}"
binary="${package_dir}/microhook"
log_path="${tmpdir}/microhook.log"

[ -x "${binary}" ] || fail "expected packaged binary at ${binary}"
[ -f "${package_dir}/README.md" ] || fail "expected packaged README.md"
[ -f "${package_dir}/docs/install.md" ] || fail "expected packaged docs/install.md"
[ -f "${package_dir}/examples/config.yml" ] || fail "expected packaged examples/config.yml"
[ -f "${package_dir}/systemd/microhook.service" ] || fail "expected packaged systemd/microhook.service"

global_token=$("${binary}" generate-token)
hello_token=$("${binary}" generate-token)
config_path="${tmpdir}/config.yml"
bad_config_path="${tmpdir}/bad-config.yml"
db_path="${tmpdir}/microhook.db"

cat > "${config_path}" <<EOF
server:
  listen: "127.0.0.1:${port}"
  log_format: "json"

auth:
  tokens:
    - name: "global"
      value: "${global_token}"
    - name: "hello-scoped"
      value: "${hello_token}"
      actions: ["hello"]

storage:
  path: "${db_path}"
  retention_days: 7
  max_runs: 100

actions:
  - name: "hello"
    description: "Packaged binary sync action"
    command: ["/bin/sh", "-c", "cat >/dev/null; printf hello-ready"]
    timeout: "5s"
    concurrency_policy: "allow"
    max_output_bytes: 1024
    enabled: true

  - name: "async"
    description: "Packaged binary async action"
    command: ["/bin/sh", "-c", "cat >/dev/null; sleep 0.2; printf async-ready"]
    timeout: "5s"
    concurrency_policy: "allow"
    max_output_bytes: 1024
    enabled: true

  - name: "serial"
    description: "Reject concurrent invocations"
    command: ["/bin/sh", "-c", "cat >/dev/null; sleep 0.5; printf serial-ready"]
    timeout: "5s"
    concurrency_policy: "reject"
    max_output_bytes: 1024
    enabled: true

  - name: "slow"
    description: "Timeout coverage"
    command: ["/bin/sh", "-c", "cat >/dev/null; printf start; sleep 1; printf end"]
    timeout: "50ms"
    concurrency_policy: "allow"
    max_output_bytes: 1024
    enabled: true

  - name: "recover"
    description: "Restart recovery coverage"
    command: ["/bin/sh", "-c", "cat >/dev/null; sleep 5; printf should-not-finish"]
    timeout: "10s"
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
  fail "expected invalid config validation to fail"
fi

start_service

unauthenticated_path="${tmpdir}/unauthenticated.json"
status=$(request_status "${unauthenticated_path}" "${base_url}/v1/runs")
assert_status "${status}" "401" "unauthenticated list runs"
assert_contains "${unauthenticated_path}" '"error":"Unauthorized"' "unauthenticated list runs response"

scoped_forbidden_path="${tmpdir}/scoped-forbidden.json"
status=$(request_status "${scoped_forbidden_path}" -H "Authorization: Bearer ${hello_token}" -H 'Content-Type: application/json' -d '{"mode":"sync"}' "${base_url}/v1/actions/async/runs")
assert_status "${status}" "403" "scoped token forbidden action"
assert_contains "${scoped_forbidden_path}" '"error":"Forbidden"' "scoped token forbidden response"

sync_path="${tmpdir}/sync.json"
status=$(request_status "${sync_path}" -H "Authorization: Bearer ${global_token}" -H 'X-Request-Id: sync-header' -H 'Content-Type: application/json' -d '{"mode":"sync","input":{"request_id":"body-id","reason":"release-e2e"}}' "${base_url}/v1/actions/hello/runs")
assert_status "${status}" "200" "sync invocation"
assert_contains "${sync_path}" '"status":"succeeded"' "sync invocation response"
assert_contains "${sync_path}" '"stdout_tail":"hello-ready"' "sync invocation response"
assert_contains "${sync_path}" '"request_id":"sync-header"' "sync invocation response"

async_accepted_path="${tmpdir}/async-accepted.json"
status=$(request_status "${async_accepted_path}" -H "Authorization: Bearer ${global_token}" -H 'X-Request-Id: async-header' -H 'Content-Type: application/json' -d '{"mode":"async","input":{"request_id":"ignored-by-header"}}' "${base_url}/v1/actions/async/runs")
assert_status "${status}" "202" "async invocation"
assert_contains "${async_accepted_path}" '"status":"running"' "async invocation response"
assert_contains "${async_accepted_path}" '"request_id":"async-header"' "async invocation response"
async_run_id=$(extract_json_string id "${async_accepted_path}")

async_completed_path="${tmpdir}/async-completed.json"
wait_for_run_status "${async_run_id}" "${global_token}" "succeeded" "${async_completed_path}"
assert_contains "${async_completed_path}" '"stdout_tail":"async-ready"' "async completed response"

bad_mode_path="${tmpdir}/bad-mode.json"
status=$(request_status "${bad_mode_path}" -H "Authorization: Bearer ${global_token}" -H 'Content-Type: application/json' -d '{"mode":"later"}' "${base_url}/v1/actions/hello/runs")
assert_status "${status}" "400" "invalid mode request"
assert_contains "${bad_mode_path}" '"error":"mode must be one of: sync, async"' "invalid mode response"

missing_action_path="${tmpdir}/missing-action.json"
status=$(request_status "${missing_action_path}" -H "Authorization: Bearer ${global_token}" -H 'Content-Type: application/json' -d '{"mode":"sync"}' "${base_url}/v1/actions/missing/runs")
assert_status "${status}" "404" "missing action request"
assert_contains "${missing_action_path}" '"error":"action not found"' "missing action response"

missing_run_path="${tmpdir}/missing-run.json"
status=$(request_status "${missing_run_path}" -H "Authorization: Bearer ${global_token}" "${base_url}/v1/runs/run_missing")
assert_status "${status}" "404" "missing run lookup"
assert_contains "${missing_run_path}" '"error":"run not found"' "missing run response"

serial_first_path="${tmpdir}/serial-first.json"
status=$(request_status "${serial_first_path}" -H "Authorization: Bearer ${global_token}" -H 'Content-Type: application/json' -d '{"mode":"async"}' "${base_url}/v1/actions/serial/runs")
assert_status "${status}" "202" "first reject-policy invocation"
serial_run_id=$(extract_json_string id "${serial_first_path}")

serial_conflict_path="${tmpdir}/serial-conflict.json"
status=$(request_status "${serial_conflict_path}" -H "Authorization: Bearer ${global_token}" -H 'Content-Type: application/json' -d '{"mode":"async"}' "${base_url}/v1/actions/serial/runs")
assert_status "${status}" "409" "second reject-policy invocation"
assert_contains "${serial_conflict_path}" '"error":"Conflict"' "reject-policy conflict response"

serial_completed_path="${tmpdir}/serial-completed.json"
wait_for_run_status "${serial_run_id}" "${global_token}" "succeeded" "${serial_completed_path}"
assert_contains "${serial_completed_path}" '"stdout_tail":"serial-ready"' "serial completed response"

slow_path="${tmpdir}/slow.json"
status=$(request_status "${slow_path}" -H "Authorization: Bearer ${global_token}" -H 'Content-Type: application/json' -d '{"mode":"sync"}' "${base_url}/v1/actions/slow/runs")
assert_status "${status}" "200" "timeout invocation"
assert_contains "${slow_path}" '"status":"timed_out"' "timeout response"
assert_contains "${slow_path}" '"timed_out":true' "timeout response"
assert_contains "${slow_path}" '"stdout_tail":"start"' "timeout response"

recover_accepted_path="${tmpdir}/recover-accepted.json"
status=$(request_status "${recover_accepted_path}" -H "Authorization: Bearer ${global_token}" -H 'Content-Type: application/json' -d '{"mode":"async"}' "${base_url}/v1/actions/recover/runs")
assert_status "${status}" "202" "restart recovery invocation"
recover_run_id=$(extract_json_string id "${recover_accepted_path}")

restart_service

recover_cancelled_path="${tmpdir}/recover-cancelled.json"
wait_for_run_status "${recover_run_id}" "${global_token}" "cancelled" "${recover_cancelled_path}"
assert_contains "${recover_cancelled_path}" '"error_summary":"service restarted before run completion"' "restart recovery response"

list_path="${tmpdir}/list.json"
status=$(request_status "${list_path}" -H "Authorization: Bearer ${global_token}" "${base_url}/v1/runs?action=async&status=succeeded")
assert_status "${status}" "200" "filtered run list"
assert_contains "${list_path}" '"id":"'"${async_run_id}"'"' "filtered run list response"
assert_contains "${list_path}" '"action":"async"' "filtered run list response"

echo "release e2e passed for ${artifact}"
