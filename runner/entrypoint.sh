#!/usr/bin/env bash
set -euo pipefail

META_DIR="/rascal-meta"
WORK_ROOT="/work"
REPO_DIR="${WORK_ROOT}/repo"
RUNNER_LOG="${META_DIR}/runner.log"
GOOSE_LOG="${META_DIR}/goose.ndjson"
META_JSON="${META_DIR}/meta.json"
INSTRUCTIONS_FILE="${META_DIR}/instructions.md"
COMMIT_MESSAGE_FILE="${META_DIR}/commit_message.txt"
RUN_START_EPOCH="$(date +%s)"

: "${RASCAL_RUN_ID:?RASCAL_RUN_ID is required}"
: "${RASCAL_TASK_ID:?RASCAL_TASK_ID is required}"
: "${RASCAL_TASK:=}"
: "${RASCAL_REPO:?RASCAL_REPO is required}"
: "${RASCAL_BASE_BRANCH:=main}"
: "${RASCAL_HEAD_BRANCH:=rascal/${RASCAL_RUN_ID}}"
: "${RASCAL_ISSUE_NUMBER:=0}"
: "${RASCAL_TRIGGER:=cli}"

mkdir -p "${META_DIR}" "${WORK_ROOT}" "${META_DIR}/goose" "${META_DIR}/codex"

log() {
  local msg="$1"
  printf '[%s] %s\n' "$(date -u +"%Y-%m-%dT%H:%M:%SZ")" "$msg"
}

format_duration() {
  local total_seconds="$1"
  local hours=$((total_seconds / 3600))
  local minutes=$(((total_seconds % 3600) / 60))
  local seconds=$((total_seconds % 60))
  local output=""

  if ((hours > 0)); then
    output+="${hours}h "
  fi
  if ((hours > 0 || minutes > 0)); then
    output+="${minutes}m "
  fi
  output+="${seconds}s"
  printf '%s' "${output% }"
}

task_subject() {
  local subject
  subject="$(printf '%s' "${RASCAL_TASK}" | tr '\r\n' ' ' | sed -E 's/[[:space:]]+/ /g; s/^ +//; s/ +$//')"
  if [[ -z "${subject}" ]]; then
    subject="${RASCAL_TASK_ID}"
  fi
  if [[ ${#subject} -gt 58 ]]; then
    subject="${subject:0:55}..."
  fi
  printf '%s' "${subject}"
}

is_conventional_title() {
  local title="$1"
  [[ "$title" =~ ^(feat|fix|docs|style|refactor|perf|test|build|ci|chore|revert)(\([a-z0-9._/-]+\))?(!)?:[[:space:]].+ ]]
}

load_agent_commit_message() {
  local path="$1"
  local title=""
  local body=""
  local line=""
  local saw_title=0

  if [[ ! -f "${path}" ]]; then
    return 0
  fi

  while IFS= read -r line || [[ -n "$line" ]]; do
    line="${line%$'\r'}"
    if [[ "${saw_title}" -eq 0 ]]; then
      if [[ -z "${line//[[:space:]]/}" ]]; then
        continue
      fi
      title="${line}"
      saw_title=1
      continue
    fi
    body+="${line}"$'\n'
  done <"${path}"

  if [[ -n "${title}" ]]; then
    if is_conventional_title "${title}"; then
      commit_title="${title}"
    else
      log "agent commit title is not conventional; using fallback title"
    fi
  fi

  body="${body%$'\n'}"
  while [[ "${body}" == $'\n'* ]]; do
    body="${body#$'\n'}"
  done
  commit_body="${body}"
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
commit_title="chore(rascal): $(task_subject)"
commit_body=""

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
  goose_args=(run --no-session -i "${INSTRUCTIONS_FILE}" --output-format stream-json)
  case "${RASCAL_GOOSE_DEBUG:-true}" in
    1|[Tt][Rr][Uu][Ee]|[Yy][Ee][Ss]|[Oo][Nn])
      goose_args+=(--debug)
      ;;
  esac
  goose "${goose_args[@]}" >"${GOOSE_LOG}"
else
  log "goose binary is required but was not found in PATH"
  printf '{"event":"error","message":"goose binary not installed"}\n' >"${GOOSE_LOG}"
  cleanup_and_exit 1 "goose binary not installed"
fi

if [[ -f Makefile ]]; then
  log "running lightweight verify: make -n test"
  make -n test >/dev/null 2>&1 || true
fi

load_agent_commit_message "${COMMIT_MESSAGE_FILE}"

if ! git diff --quiet || ! git diff --cached --quiet; then
  git add -A
  final_commit_body="${commit_body}"
  if [[ -n "${final_commit_body}" ]]; then
    final_commit_body+=$'\n\n'
  fi
  final_commit_body+="Run: ${RASCAL_RUN_ID}"
  git -c user.name="rascal-bot" -c user.email="rascal-bot@users.noreply.github.com" \
    commit -m "${commit_title}" -m "${final_commit_body}" || true
fi

if [[ -n "${GH_TOKEN:-}" ]]; then
  log "pushing branch"
  git push -u origin "${RASCAL_HEAD_BRANCH}" || cleanup_and_exit 1 "git push failed"

  if command -v gh >/dev/null 2>&1; then
    if pr_view_json="$(gh pr view "${RASCAL_HEAD_BRANCH}" --repo "${RASCAL_REPO}" --json number,url 2>/dev/null)"; then
      pr_number="$(jq -r '.number // 0' <<<"$pr_view_json")"
      pr_url="$(jq -r '.url // ""' <<<"$pr_view_json")"
    else
      log "creating pull request"
      pr_body="Automated changes from Rascal run ${RASCAL_RUN_ID}."
      if [[ -n "${commit_body}" ]]; then
        pr_body="${commit_body}"$'\n\n'"${pr_body}"
      fi
      if [[ -s "${GOOSE_LOG}" ]]; then
        goose_output="$(cat "${GOOSE_LOG}")"
      else
        goose_output="(no goose output captured)"
      fi
      goose_section=$'<details><summary>Run Details</summary>\n\n```\n'"${goose_output}"$'\n```\n\n</details>'
      pr_body="${pr_body}"$'\n\n'"${goose_section}"
      if [[ "${RASCAL_ISSUE_NUMBER}" =~ ^[0-9]+$ ]] && [[ "${RASCAL_ISSUE_NUMBER}" -gt 0 ]]; then
        pr_body="${pr_body}"$'\n\n'"Closes #${RASCAL_ISSUE_NUMBER}"
      fi
      run_duration_seconds="$(( $(date +%s) - RUN_START_EPOCH ))"
      run_duration="$(format_duration "${run_duration_seconds}")"
      pr_body="${pr_body}"$'\n\n---\n\n'"Rascal run took ${run_duration}"
      if ! pr_create_output="$(gh pr create \
        --repo "${RASCAL_REPO}" \
        --base "${RASCAL_BASE_BRANCH}" \
        --head "${RASCAL_HEAD_BRANCH}" \
        --title "${commit_title}" \
        --body "${pr_body}" 2>&1)"; then
        cleanup_and_exit 1 "gh pr create failed: ${pr_create_output}"
      fi

      if pr_view_json="$(gh pr view "${RASCAL_HEAD_BRANCH}" --repo "${RASCAL_REPO}" --json number,url 2>/dev/null)"; then
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
