package main

const zshIntegrationScript = `_fzr-directory-matches() {
    emulate -L zsh

    local path_spec="$1" literal="$2" path_value
    path_value="${(Q)path_spec}"
    reply=()

    if [[ -n "${literal}" ]]; then
        # Quoting or a backslash makes glob metacharacters literal. Preserve
        # that distinction after removing shell syntax for the filesystem test.
        # An unquoted ~/ prefix still follows normal shell tilde behavior.
        if [[ "${path_spec}" == '~/'* ]]; then
            path_value="${HOME}/${path_value[3,-1]}"
        fi
        if [[ -d "${path_value}" ]]; then
            reply=( "${path_value}" )
        fi
    else
        reply=( ${~path_value}(N-/) )
    fi
}

_fzr-path-context-for-buffer() {
    emulate -L zsh
    autoload -Uz split-shell-arguments

    local current_word current_value path_part path_value
    local path_literal assignment_position=1
    local -i current_index word_index
    local -a split_reply dir_matches

    FZR_SEARCH_IN='.'
    FZR_APPEND_SEARCH_SLASH=''
    FZR_SEARCH_LITERAL=''

    if [[ -z "${LBUFFER}" || "${LBUFFER[-1]}" == [[:space:]] ]]; then
        return 0
    fi

    local reply REPLY
    # Let zsh parse the buffer instead of guessing at quoting or escapes.
    split-shell-arguments
    split_reply=( "${reply[@]}" )
    current_index=$(( REPLY - 1 ))
    current_word="${split_reply[current_index]}"
    current_value="${(Q)current_word}"

    # NAME=value is assignment syntax only before the command word. Once a
    # command appears, '=' belongs to an ordinary path such as eq=dir.
    for (( word_index = 2; word_index < current_index; word_index += 2 )); do
        if [[ ! "${(Q)split_reply[word_index]}" =~ '^[A-Za-z_][A-Za-z0-9_]*=' ]]; then
            assignment_position=''
            break
        fi
    done
    if [[ -n "${assignment_position}" && "${current_value}" =~ '^[A-Za-z_][A-Za-z0-9_]*=' ]]; then
        path_part="${current_word#*=}"
    else
        path_part="${current_word}"
    fi
    path_value="${(Q)path_part}"
    if [[ "${path_part}" != "${path_value}" ]]; then
        path_literal=1
        FZR_SEARCH_LITERAL=1
    fi

    if [[ "${path_value[-1]}" == "/" ]]; then
        if [[ -n "${path_part}" ]]; then
            FZR_SEARCH_IN="${path_part}"
        fi
    elif [[ -n "${path_part}" ]]; then
        # Expand a path-like word only when it resolves to one directory;
        # ambiguous globs should not choose a search root for the user.
        _fzr-directory-matches "${path_part}" "${path_literal}"
        dir_matches=( "${reply[@]}" )
        if (( ${#dir_matches} == 1 )); then
            FZR_APPEND_SEARCH_SLASH=1
            FZR_SEARCH_IN="${path_part}"
        fi
    fi
}

fzr-append-path-to-buffer() {
    emulate -L zsh

    local search_in search_literal selected_path append_search_slash fzr_status
    local -a reply dir_matches

    zle -I
    # Clear generic zle suffix display state before changing LBUFFER so wrapper
    # widgets cannot leave stale prompt text after fzr returns.
    POSTDISPLAY=
    fzr_status=0

    # Only a word touching the cursor is path context. split-shell-arguments
    # reports the previous word while the cursor is in separator whitespace, but
    # Ctrl-F there should insert another argument rather than append to the
    # completed one.
    local FZR_SEARCH_IN FZR_APPEND_SEARCH_SLASH FZR_SEARCH_LITERAL
    _fzr-path-context-for-buffer
    search_in="${FZR_SEARCH_IN}"
    search_literal="${FZR_SEARCH_LITERAL}"
    append_search_slash="${FZR_APPEND_SEARCH_SLASH}"

    _fzr-directory-matches "${search_in}" "${search_literal}"
    dir_matches=( "${reply[@]}" )
    if (( ${#dir_matches} != 1 )); then
        if (( ${#dir_matches} == 0 )); then
            zle -M "fzr: ${search_in} is not a directory"
        else
            zle -M "fzr: ${search_in} matches more than one directory"
        fi
        fzr_status=1
    else
        search_in="${dir_matches[1]}"

        # Keep fzr attached to the terminal even though command substitution is
        # capturing stdout for the selected path.
        selected_path="$(fzr -i --ignore-common -- "${search_in}" </dev/tty)"
        fzr_status=$?
        if (( fzr_status == 0 )) && [[ -d "${search_in}/${selected_path}" ]]; then
            selected_path+="/"
        fi
    fi

    if (( fzr_status == 0 )) && [[ -n "${selected_path}" ]]; then
        if [[ -n "${append_search_slash}" ]]; then
            LBUFFER+="/"
        fi
        if [[ "${LBUFFER[-1]}" =~ [[:alnum:]] ]]; then
            LBUFFER+=" "
        fi
        LBUFFER+="${(q)selected_path}"
    fi
    # fzr writes directly to the terminal while zle is active; ask zle to
    # re-expand and redraw this edit buffer instead of leaving prompt recovery
    # to happen on a fresh line.
    zle reset-prompt
    return "${fzr_status}"
}

# Use a normal public widget name so other zle wrappers can treat this as a
# regular buffer-modifying widget.
zle -N fzr-append-path-to-buffer
bindkey "^F" fzr-append-path-to-buffer
`
