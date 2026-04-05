#!/usr/bin/env bash
set -uo pipefail

ROOT="${1:-$(git -C "$(dirname "$0")" rev-parse --show-toplevel 2>/dev/null || dirname "$(dirname "$0")")}"

if [ -t 1 ]; then
  BOLD='\033[1m' DIM='\033[2m' CYAN='\033[36m' GREEN='\033[32m'
  YELLOW='\033[33m' RESET='\033[0m'
else
  BOLD='' DIM='' CYAN='' GREEN='' YELLOW='' RESET=''
fi

collect_files() {
  local pattern="$1" out="$2"
  if git -C "$ROOT" rev-parse --is-inside-work-tree &>/dev/null; then
    git -C "$ROOT" ls-files -- "$pattern" 2>/dev/null | while IFS= read -r f; do
      echo "$ROOT/$f"
    done > "$out"
  else
    find "$ROOT" -name "$pattern" -not -path '*/.git/*' -not -path '*/vendor/*' \
      -not -path '*/node_modules/*' 2>/dev/null > "$out"
  fi
}

count_code_lines() {
  local style="$1" filelist="$2"

  if [ ! -s "$filelist" ]; then
    echo 0
    return
  fi

  case "$style" in
    slash)
      cat $(cat "$filelist") 2>/dev/null \
        | sed 's|//.*||' \
        | perl -0777 -pe 's{/\*.*?\*/}{}gs' \
        | grep -cvE '^\s*$' || echo 0
      ;;
    hash)
      cat $(cat "$filelist") 2>/dev/null \
        | sed 's/#.*$//' \
        | grep -cvE '^\s*$' || echo 0
      ;;
    sql)
      cat $(cat "$filelist") 2>/dev/null \
        | sed 's/--.*$//' \
        | perl -0777 -pe 's{/\*.*?\*/}{}gs' \
        | grep -cvE '^\s*$' || echo 0
      ;;
    none)
      cat $(cat "$filelist") 2>/dev/null \
        | grep -cvE '^\s*$' || echo 0
      ;;
  esac
}

file_count() {
  if [ -s "$1" ]; then
    wc -l < "$1" | tr -d ' '
  else
    echo 0
  fi
}

TMPDIR_LOC=$(mktemp -d)
trap 'rm -rf "$TMPDIR_LOC"' EXIT

collect_files '*.go'         "$TMPDIR_LOC/go"
collect_files '*.sh'         "$TMPDIR_LOC/sh"
collect_files '*.sql'        "$TMPDIR_LOC/sql"
collect_files '*.py'         "$TMPDIR_LOC/py"
collect_files '*.yml'        "$TMPDIR_LOC/yaml1"
collect_files '*.yaml'       "$TMPDIR_LOC/yaml2"
cat "$TMPDIR_LOC/yaml1" "$TMPDIR_LOC/yaml2" > "$TMPDIR_LOC/yaml"
collect_files 'Dockerfile*'  "$TMPDIR_LOC/docker"
collect_files '*.toml'       "$TMPDIR_LOC/toml"

go_loc=$(count_code_lines slash "$TMPDIR_LOC/go")
sh_loc=$(count_code_lines hash  "$TMPDIR_LOC/sh")
sql_loc=$(count_code_lines sql  "$TMPDIR_LOC/sql")
py_loc=$(count_code_lines hash  "$TMPDIR_LOC/py")
yaml_loc=$(count_code_lines hash "$TMPDIR_LOC/yaml")
docker_loc=$(count_code_lines hash "$TMPDIR_LOC/docker")
toml_loc=$(count_code_lines hash "$TMPDIR_LOC/toml")

go_count=$(file_count "$TMPDIR_LOC/go")
sh_count=$(file_count "$TMPDIR_LOC/sh")
sql_count=$(file_count "$TMPDIR_LOC/sql")
py_count=$(file_count "$TMPDIR_LOC/py")
yaml_count=$(file_count "$TMPDIR_LOC/yaml")
docker_count=$(file_count "$TMPDIR_LOC/docker")
toml_count=$(file_count "$TMPDIR_LOC/toml")

total=$((go_loc + sh_loc + sql_loc + py_loc + yaml_loc + docker_loc + toml_loc))
total_files=$((go_count + sh_count + sql_count + py_count + yaml_count + docker_count + toml_count))

printf "\n${BOLD}${CYAN}  Lines of Code (comments & blanks excluded)${RESET}\n"
printf "${DIM}  %-14s %6s  %5s  %s${RESET}\n" "Language" "LOC" "%" "Files"
printf "  ${DIM}─────────────────────────────────────────────${RESET}\n"

print_row() {
  local lang="$1" loc="$2" count="$3" colour="$4"
  if [ "$loc" -gt 0 ]; then
    local pct
    pct=$(awk "BEGIN { printf \"%.1f\", ($loc / $total) * 100 }")
    printf "  ${colour}%-14s${RESET} %6d  %5s%%  %d files\n" "$lang" "$loc" "$pct" "$count"
  fi
}

print_row "Go"         "$go_loc"     "$go_count"     "$GREEN"
print_row "Shell"      "$sh_loc"     "$sh_count"     "$YELLOW"
print_row "SQL"        "$sql_loc"    "$sql_count"    "$CYAN"
print_row "Python"     "$py_loc"     "$py_count"     "$GREEN"
print_row "YAML"       "$yaml_loc"   "$yaml_count"   "$YELLOW"
print_row "Dockerfile" "$docker_loc" "$docker_count" "$CYAN"
print_row "TOML"       "$toml_loc"   "$toml_count"   "$GREEN"

printf "  ${DIM}─────────────────────────────────────────────${RESET}\n"
printf "  ${BOLD}%-14s %6d${RESET}         %d files\n\n" "Total" "$total" "$total_files"
