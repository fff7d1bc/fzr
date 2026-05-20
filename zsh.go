package main

const zshIntegrationScript = `fzr-append-path-to-buffer() {
    emulate -L zsh
    autoload -Uz split-shell-arguments

    local search_in selected_path append_search_slash fzr_status
    local current_word path_part
    local -a split_reply dir_matches

    zle -I
    # Clear generic zle suffix display state before changing LBUFFER so wrapper
    # widgets cannot leave stale prompt text after fzr returns.
    POSTDISPLAY=
    fzr_status=0

    search_in='.'
    append_search_slash=''
    if [[ -n "${LBUFFER}" ]]; then
        local reply REPLY
        # Let zsh parse the buffer instead of guessing at quoting or escapes.
        split-shell-arguments
        split_reply=( "${reply[@]}" )
        current_word="${split_reply[REPLY - 1]}"
        path_part="${current_word##*=}"

        if [[ "${LBUFFER[-1]}" == "/" ]]; then
            if [[ -n "${path_part}" ]]; then
                search_in="${path_part}"
            fi
        elif [[ -n "${path_part}" ]]; then
            # Expand a path-like word only when it resolves to one directory;
            # ambiguous globs should not choose a search root for the user.
            dir_matches=( ${~${(Q)path_part}}(N-/) )
            if (( ${#dir_matches} == 1 )); then
                append_search_slash=1
                search_in="${path_part}"
            fi
        fi
    fi

    dir_matches=( ${~${(Q)search_in}}(N-/) )
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
    zle -R
    return "${fzr_status}"
}

# Use a normal public widget name so other zle wrappers can treat this as a
# regular buffer-modifying widget.
zle -N fzr-append-path-to-buffer
bindkey "^F" fzr-append-path-to-buffer
`
