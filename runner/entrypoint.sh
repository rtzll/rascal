#!/usr/bin/env bash
set -euo pipefail

META_DIR="/rascal-meta"
WORK_ROOT="/work"
REPO_DIR="${WORK_ROOT}/repo"
RUNNER_LOG="${META_DIR}/runner.log"
GOOSE_LOG="${META_DIR}/goose.ndjson"
META_JSON="${META_DIR}/meta.json"
INSTRUCTIONS_FILE="${META_DIR}/instructions.md"

: "${RASCAL_RUN_ID:?RASCAL_RUN_ID is required}"
: "${RASCAL_TASK_ID:?RASCAL_TASK_ID is required}"
: "${RASCAL_TASK:=}"
: "${RASCAL_REPO:?RASCAL_REPO is required}"
: "${RASCAL_BASE_BRANCH:=main}"
: "${RASCAL_HEAD_BRANCH:=rascal/${RASCAL_RUN_ID}}"
: "${RASCAL_TRIGGER:=cli}"

mkdir -p "${META_DIR}" "${WORK_ROOT}" "${META_DIR}/goose" "${META_DIR}/codex"

log() {
  local msg="$1"
  printf '[%s] %s\n' "$(date -u +"%Y-%m-%dT%H:%M:%SZ")" "$msg"
}

task_subject() {
  local subject
  subject="$(printf '%s' "${RASCAL_TASK}" | tr '\r\n' ' ' | sed -E 's/[[:space:]]+/ /g; s/^ +//; s/ +$//')"
  if [[ -z "${subject}" ]]; then
    subject="${RASCAL_TASK_ID}"
  fi
  if [[ ${#subject} -gt 72 ]]; then
    subject="${subject:0:69}..."
  fi
  printf '%s' "${subject}"
}

write_meta() {
  local exit_code="$1"
  local pr_number="$2"
  local pr_url="$3"
  local head_sha="$4"
  local error_text="${5:-}"

  jq -n \
    --arg run_id "${RASCAL_RUN_ID}" \
    --arg task_id "${RASCAL_TASK_ID}" \
    --arg repo "${RASCAL_REPO}" \
    --arg base_branch "${RASCAL_BASE_BRANCH}" \
    --arg head_branch "${RASCAL_HEAD_BRANCH}" \
    --arg pr_url "${pr_url}" \
    --arg head_sha "${head_sha}" \
    --arg error "${error_text}" \
    --argjson pr_number "${pr_number}" \
    --argjson exit_code "${exit_code}" \
    '{
      run_id: $run_id,
      task_id: $task_id,
      repo: $repo,
      base_branch: $base_branch,
      head_branch: $head_branch,
      pr_number: $pr_number,
      pr_url: $pr_url,
      head_sha: $head_sha,
      exit_code: $exit_code,
      error: $error
    }' >"${META_JSON}"
}

repo_url="https://github.com/${RASCAL_REPO}.git"
if [[ -n "${GH_TOKEN:-}" ]]; then
  repo_url="https://x-access-token:${GH_TOKEN}@github.com/${RASCAL_REPO}.git"
fi

if [[ ! -f "${INSTRUCTIONS_FILE}" ]]; then
  cat >"${INSTRUCTIONS_FILE}" <<EOT
# Rascal Instructions

Task ID: ${RASCAL_TASK_ID}
Trigger: ${RASCAL_TRIGGER}

Follow the repository instructions and implement the requested task.
Keep changes minimal, run tests, and summarize what changed.
EOT
fi

log "run started run_id=${RASCAL_RUN_ID} repo=${RASCAL_REPO}"

pr_number=0
pr_url=""
head_sha=""
commit_title="rascal: $(task_subject)"

cleanup_and_exit() {
  local code="$1"
  local err="${2:-}"
  write_meta "$code" "$pr_number" "$pr_url" "$head_sha" "$err"
  if [[ "$code" -ne 0 ]]; then
    log "run failed exit_code=${code} error=${err}"
  else
    log "run completed exit_code=${code}"
  fi
  exit "$code"
}

trap 'cleanup_and_exit 1 "unexpected error (line ${LINENO})"' ERR

if [[ -d "${REPO_DIR}/.git" ]]; then
  log "repo already present, refreshing"
  git -C "${REPO_DIR}" fetch --all --prune
else
  log "cloning ${RASCAL_REPO}"
  git clone "${repo_url}" "${REPO_DIR}"
fi

cd "${REPO_DIR}"
git fetch origin "${RASCAL_BASE_BRANCH}" "${RASCAL_HEAD_BRANCH}" || true
git checkout "${RASCAL_BASE_BRANCH}" || git checkout -b "${RASCAL_BASE_BRANCH}" "origin/${RASCAL_BASE_BRANCH}"
git pull --ff-only origin "${RASCAL_BASE_BRANCH}" || true

if git rev-parse --verify "${RASCAL_HEAD_BRANCH}" >/dev/null 2>&1; then
  git checkout "${RASCAL_HEAD_BRANCH}"
elif git ls-remote --exit-code --heads origin "${RASCAL_HEAD_BRANCH}" >/dev/null 2>&1; then
  git checkout -b "${RASCAL_HEAD_BRANCH}" "origin/${RASCAL_HEAD_BRANCH}"
else
  git checkout -b "${RASCAL_HEAD_BRANCH}"
fi

if command -v goose >/dev/null 2>&1; then
  log "running goose"
  goose run --no-session -i "${INSTRUCTIONS_FILE}" --output-format stream-json >"${GOOSE_LOG}"
else
  log "goose binary is required but was not found in PATH"
  printf '{"event":"error","message":"goose binary not installed"}\n' >"${GOOSE_LOG}"
  cleanup_and_exit 1 "goose binary not installed"
fi

if [[ -f Makefile ]]; then
  log "running lightweight verify: make -n test"
  make -n test >/dev/null 2>&1 || true
fi

if ! git diff --quiet || ! git diff --cached --quiet; then
  git add -A
  git -c user.name="rascal-bot" -c user.email="rascal-bot@users.noreply.github.com" \
    commit -m "${commit_title}" -m "Run: ${RASCAL_RUN_ID}" || true
fi

if [[ -n "${GH_TOKEN:-}" ]]; then
  log "pushing branch"
  git push -u origin "${RASCAL_HEAD_BRANCH}" || cleanup_and_exit 1 "git push failed"

  if command -v gh >/dev/null 2>&1; then
    set +e
    pr_view_json="$(gh pr view "${RASCAL_HEAD_BRANCH}" --repo "${RASCAL_REPO}" --json number,url 2>/dev/null)"
    rc=$?
    set -e
    if [[ $rc -eq 0 && -n "$pr_view_json" ]]; then
      pr_number="$(jq -r '.number // 0' <<<"$pr_view_json")"
      pr_url="$(jq -r '.url // ""' <<<"$pr_view_json")"
    else
      log "creating pull request"
      set +e
      pr_create_output="$(gh pr create \
        --repo "${RASCAL_REPO}" \
        --base "${RASCAL_BASE_BRANCH}" \
        --head "${RASCAL_HEAD_BRANCH}" \
        --title "${commit_title}" \
        --body "Automated changes from Rascal run ${RASCAL_RUN_ID}." 2>&1)"
      rc=$?
      set -e
      if [[ $rc -ne 0 ]]; then
        cleanup_and_exit 1 "gh pr create failed: ${pr_create_output}"
      fi

      set +e
      pr_view_json="$(gh pr view "${RASCAL_HEAD_BRANCH}" --repo "${RASCAL_REPO}" --json number,url 2>/dev/null)"
      rc=$?
      set -e
      if [[ $rc -eq 0 && -n "$pr_view_json" ]]; then
        pr_number="$(jq -r '.number // 0' <<<"$pr_view_json")"
        pr_url="$(jq -r '.url // ""' <<<"$pr_view_json")"
      else
        pr_url="$(printf '%s\n' "${pr_create_output}" | rg -o 'https://github.com/[^[:space:]]+/pull/[0-9]+' -m1 || true)"
        if [[ -n "${pr_url}" ]]; then
          pr_number="${pr_url##*/}"
        fi
      fi
    fi
  fi
fi

head_sha="$(git rev-parse HEAD || true)"
cleanup_and_exit 0 ""
