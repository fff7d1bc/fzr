package main

const zshIntegrationScript = `_fzr_append_path_to_buffer() {
    autoload -Uz split-shell-arguments

    local search_in selected_path append_search_slash
    local current_word path_part
    local -a split_reply dir_matches

    zle -I
    print -nr "${zle_bracketed_paste[2]}" >/dev/tty
    {
        search_in='.'
        append_search_slash=''
        if [[ -n "${LBUFFER}" ]]; then
            local reply REPLY
            split-shell-arguments
            split_reply=( "${reply[@]}" )
            current_word="${split_reply[REPLY - 1]}"
            path_part="${current_word##*=}"

            if [[ "${LBUFFER[-1]}" == "/" ]]; then
                if [[ -n "${path_part}" ]]; then
                    search_in="${path_part}"
                fi
            elif [[ -n "${path_part}" ]]; then
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
            return 1
        fi
        search_in="${dir_matches[1]}"

        selected_path="$(fzr -i --ignore-common -- "${search_in}" </dev/tty)" || return
        if [[ -d "${search_in}/${selected_path}" ]]; then
            selected_path+="/"
        fi
    } always {
        print -nr "${zle_bracketed_paste[1]}" >/dev/tty
    }

    if [[ -n "${selected_path}" ]]; then
        if [[ -n "${append_search_slash}" ]]; then
            LBUFFER+="/"
        fi
        if [[ "${LBUFFER[-1]}" =~ [[:alnum:]] ]]; then
            LBUFFER+=" "
        fi
        LBUFFER+="${(q)selected_path}"
    fi
    zle reset-prompt
}

zle -N _fzr_append_path_to_buffer
bindkey "^F" _fzr_append_path_to_buffer
`
