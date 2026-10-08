#!/bin/sh
#========================================================
#   System Required: CentOS 7+ / Debian 8+ / Ubuntu 16+ / Alpine / macOS
#   Arch 未测试
#   Description: 探针安装脚本
#   Github: https://github.com/xOS/ServerStatus
#========================================================

# 若系统已安装 bash，优先切入 bash 运行以获得更佳交互体验；若无 bash，则直接以当前 POSIX / BusyBox ash 运行，避免占用额外空间
if [ -z "$BASH_VERSION" ]; then
    if command -v bash >/dev/null 2>&1; then
        if [ -f "$0" ]; then
            exec bash "$0" "$@"
        fi
    fi
fi

BASE_PATH="/opt/server-status"
DASHBOARD_PATH="${BASE_PATH}/dashboard"
AGENT_PATH="${BASE_PATH}/agent"
AGENT_SERVICE="/etc/systemd/system/server-agent.service"
AGENT_CONFIG="${AGENT_PATH}/config.yml"
AGENT_OPENRC_SERVICE="/etc/init.d/server-agent"
AGENT_LAUNCHD_SERVICE="$HOME/Library/LaunchAgents/com.serverstatus.agent.plist"
VERSION="v0.4.3"

red='\033[0;31m'
green='\033[0;32m'
yellow='\033[0;33m'
plain='\033[0m'
export PATH=$PATH:/usr/local/bin:/sbin:/usr/sbin

os_arch=""
os_alpine=0
os_macos=0

sudo() {
    myEUID=$(id -u 2>/dev/null || echo "$EUID")
    if [ "$myEUID" -ne 0 ]; then
        if command -v sudo > /dev/null 2>&1; then
            command sudo "$@"
        elif command -v doas > /dev/null 2>&1; then
            command doas "$@"
        else
            err "错误: 您的系统未安装 sudo 或 doas，因此无法进行该项操作。"
            exit 1
        fi
    else
        "$@"
    fi
}

init_openrc_env() {
    if [ "$os_alpine" = 1 ]; then
        sudo mkdir -p /run/openrc
        [ -f /run/openrc/softlevel ] || sudo touch /run/openrc/softlevel
        # 兼容旧版本：检查已存在的 OpenRC 脚本是否将错误流单独输出至 _error.log
        if [ -f "$AGENT_OPENRC_SERVICE" ]; then
            if grep -q 'error_log="/var/log/\${name}_error.log"' "$AGENT_OPENRC_SERVICE" 2>/dev/null; then
                sed -i 's#error_log="/var/log/\${name}_error.log"#error_log="/var/log/\${name}.log"#g' "$AGENT_OPENRC_SERVICE" 2>/dev/null || true
            fi
        fi
        # 迁移旧版本残留的错误日志
        if [ -s /var/log/server-agent_error.log ]; then
            if [ ! -s /var/log/server-agent.log ]; then
                cat /var/log/server-agent_error.log >> /var/log/server-agent.log 2>/dev/null || true
            fi
            rm -f /var/log/server-agent_error.log 2>/dev/null || true
        fi
    fi
}

check_systemd() {
    if [ "$os_alpine" != 1 ] && ! command -v systemctl >/dev/null 2>&1; then
        echo "不支持此系统：未找到 systemctl 命令"
        exit 1
    fi
}

# 服务管理辅助函数
service_enable() {
    if [ "$os_alpine" = 1 ]; then
        init_openrc_env
        rc-update add server-agent default 2>/dev/null || true
    elif [ "$os_macos" = 1 ]; then
        # macOS使用用户级LaunchAgent
        echo "正在加载LaunchAgent..."
        launchctl unload $AGENT_LAUNCHD_SERVICE 2>/dev/null || true
        launchctl load $AGENT_LAUNCHD_SERVICE 2>/dev/null || true
        launchctl enable gui/$(id -u)/com.serverstatus.agent 2>/dev/null || true
    else
        systemctl enable server-agent
    fi
}

service_start() {
    if [ "$os_alpine" = 1 ]; then
        init_openrc_env
        rc-service server-agent start
    elif [ "$os_macos" = 1 ]; then
        # macOS LaunchAgent通过load自动启动，如果没有启动则手动启动
        if ! launchctl list | grep com.serverstatus.agent >/dev/null 2>&1; then
            launchctl load $AGENT_LAUNCHD_SERVICE 2>/dev/null || true
        fi
        launchctl start com.serverstatus.agent 2>/dev/null || true
    else
        systemctl start server-agent
    fi
}

service_stop() {
    if [ "$os_alpine" = 1 ]; then
        init_openrc_env
        rc-service server-agent stop 2>/dev/null || true
        if [ -f /run/server-agent.pid ]; then
            local pid=$(cat /run/server-agent.pid 2>/dev/null)
            if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
                kill "$pid" 2>/dev/null || true
                sleep 1
                kill -9 "$pid" 2>/dev/null || true
            fi
            rm -f /run/server-agent.pid
        fi
    elif [ "$os_macos" = 1 ]; then
        launchctl stop com.serverstatus.agent
    else
        systemctl stop server-agent
    fi
}

service_restart() {
    if [ "$os_alpine" = 1 ]; then
        init_openrc_env
        if rc-service server-agent status >/dev/null 2>&1; then
            rc-service server-agent restart
        else
            rc-service server-agent start
        fi
    elif [ "$os_macos" = 1 ]; then
        echo "正在重启探针服务..."
        launchctl stop com.serverstatus.agent 2>/dev/null || true
        sleep 2
        # 确保plist已加载
        if ! launchctl list | grep com.serverstatus.agent >/dev/null 2>&1; then
            launchctl load $AGENT_LAUNCHD_SERVICE 2>/dev/null || true
        fi
        launchctl start com.serverstatus.agent 2>/dev/null || true
    else
        systemctl restart server-agent
    fi
}

service_status() {
    if [ "$os_alpine" = 1 ]; then
        init_openrc_env
        rc-service server-agent status
    elif [ "$os_macos" = 1 ]; then
        launchctl list | grep com.serverstatus.agent || echo "服务未运行"
    else
        systemctl status server-agent
    fi
}

service_disable() {
    if [ "$os_alpine" = 1 ]; then
        init_openrc_env
        rc-update del server-agent default 2>/dev/null || true
    elif [ "$os_macos" = 1 ]; then
        launchctl disable gui/$(id -u)/com.serverstatus.agent 2>/dev/null || true
        launchctl unload $AGENT_LAUNCHD_SERVICE 2>/dev/null || true
    else
        systemctl disable server-agent
    fi
}

daemon_reload() {
    if [ "$os_alpine" != 1 ] && [ "$os_macos" != 1 ]; then
        systemctl daemon-reload
    fi
}

err() {
    printf "${red}$*${plain}\n" >&2
}

LAST_DOWNLOAD_ERROR=""

set_download_error() {
    LAST_DOWNLOAD_ERROR="$*"
    if [ -n "$*" ]; then
        echo "$*" > "/tmp/server_status_last_dl_err_$$.log" 2>/dev/null || true
    else
        rm -f "/tmp/server_status_last_dl_err_$$.log" 2>/dev/null || true
    fi
}

get_download_error() {
    if [ -n "$LAST_DOWNLOAD_ERROR" ]; then
        echo "$LAST_DOWNLOAD_ERROR"
    elif [ -s "/tmp/server_status_last_dl_err_$$.log" ]; then
        cat "/tmp/server_status_last_dl_err_$$.log" 2>/dev/null
    else
        echo "未能建立网络连接或未知网络错误"
    fi
}

download_file() {
    local url="$1"
    local output="$2"
    local timeout="${3:-30}"
    local err_file="/tmp/dl_err_$$.log"
    rm -f "$err_file"
    set_download_error ""

    # 1. 尝试使用 curl
    if command -v curl >/dev/null 2>&1; then
        if curl -fSL -m "$timeout" "$url" -o "$output" 2>"$err_file"; then
            rm -f "$err_file"
            set_download_error ""
            return 0
        else
            local curl_code=$?
            local err_msg=""
            [ -f "$err_file" ] && err_msg=$(head -n 2 "$err_file" | tr '\n' ' ' | sed 's/  */ /g')
            set_download_error "curl 错误 (退出码 ${curl_code}): ${err_msg:-网络连接失败或超时}"
        fi
    fi

    # 2. 尝试使用 wget
    if command -v wget >/dev/null 2>&1; then
        if wget -T "$timeout" -O "$output" "$url" 2>"$err_file"; then
            rm -f "$err_file"
            set_download_error ""
            return 0
        elif wget -O "$output" "$url" 2>"$err_file"; then
            rm -f "$err_file"
            set_download_error ""
            return 0
        else
            local wget_code=$?
            local err_msg=""
            [ -f "$err_file" ] && err_msg=$(head -n 2 "$err_file" | tr '\n' ' ' | sed 's/  */ /g')
            set_download_error "wget 错误 (退出码 ${wget_code}): ${err_msg:-网络连接失败或超时}"
        fi
    fi

    # 3. Alpine 特殊处理：若自带 wget 因缺少根证书失败，按需补充安装 ca-certificates 或 curl 重试
    if [ "$os_alpine" = 1 ]; then
        if [ ! -f /etc/ssl/certs/ca-certificates.crt ]; then
            echo -e "${yellow}[自动修复] 检测到缺失 SSL 根证书，正在安装 ca-certificates...${plain}" >&2
            install_soft ca-certificates
            command -v update-ca-certificates >/dev/null 2>&1 && sudo update-ca-certificates >/dev/null 2>&1 || true
            if command -v wget >/dev/null 2>&1 && wget -T "$timeout" -O "$output" "$url" 2>"$err_file"; then
                rm -f "$err_file"
                set_download_error ""
                return 0
            fi
        fi
        if ! command -v curl >/dev/null 2>&1; then
            echo -e "${yellow}[自动修复] 原生 wget 下载失败，正在按需安装 curl 进行重试...${plain}" >&2
            if install_soft curl; then
                if curl -fSL -m "$timeout" "$url" -o "$output" 2>"$err_file"; then
                    rm -f "$err_file"
                    set_download_error ""
                    return 0
                else
                    local retry_code=$?
                    local err_msg=""
                    [ -f "$err_file" ] && err_msg=$(head -n 2 "$err_file" | tr '\n' ' ' | sed 's/  */ /g')
                    set_download_error "curl 重试错误 (退出码 ${retry_code}): ${err_msg:-网络连接失败或超时}"
                fi
            fi
        fi
    fi

    if ! command -v curl >/dev/null 2>&1 && ! command -v wget >/dev/null 2>&1; then
        set_download_error "系统未找到 curl 或 wget 下载工具"
    fi

    rm -f "$err_file"
    return 1
}

get_agent_version() {
    local tmp_json="/tmp/agent_ver_$$.json"
    local ver=""
    local r2_err=""
    local gh_err=""
    rm -f "$tmp_json"

    # 中国大陆优先查询 R2 镜像节点
    if [ -n "$CN" ]; then
        local r2_url="${R2_URL:-https://assets.cnic.eu.org}/serveragent/index.json"
        if download_file "$r2_url" "$tmp_json" 10 >/dev/null; then
            ver=$(grep -o '"tag_name":"[^"]*"' "$tmp_json" 2>/dev/null | head -n 1 | awk -F '"' '{print $4}')
            [ -z "$ver" ] && r2_err="R2 响应内容未包含 tag_name"
        else
            r2_err="$(get_download_error)"
        fi
        rm -f "$tmp_json"
    fi

    # 查询 GitHub Releases API
    if [ -z "$ver" ]; then
        local gh_url="https://api.github.com/repos/xos/serveragent/releases/latest"
        if download_file "$gh_url" "$tmp_json" 10 >/dev/null; then
            ver=$(grep "tag_name" "$tmp_json" 2>/dev/null | head -n 1 | awk -F ":" '{print $2}' | sed 's/\"//g;s/,//g;s/ //g')
            [ -z "$ver" ] && gh_err="GitHub API 响应内容未包含 tag_name"
        else
            gh_err="$(get_download_error)"
        fi
        rm -f "$tmp_json"
    fi

    # 兜底：若前两者未成功且非CN，再尝试R2
    if [ -z "$ver" ] && [ -z "$CN" ]; then
        local r2_url="${R2_URL:-https://assets.cnic.eu.org}/serveragent/index.json"
        if download_file "$r2_url" "$tmp_json" 10 >/dev/null; then
            ver=$(grep -o '"tag_name":"[^"]*"' "$tmp_json" 2>/dev/null | head -n 1 | awk -F '"' '{print $4}')
            [ -z "$ver" ] && r2_err="R2 响应内容未包含 tag_name"
        else
            r2_err="$(get_download_error)"
        fi
        rm -f "$tmp_json"
    fi

    if [ -z "$ver" ]; then
        set_download_error "探针版本查询失败 (R2: ${r2_err:-未响应}; GitHub API: ${gh_err:-未响应})"
    else
        set_download_error ""
    fi

    echo "$ver"
}

download_release_archive() {
    local primary_url="$1"
    local fallback_url="$2"
    local output_file="$3"

    echo -e "正在从节点下载: ${primary_url}"
    if download_file "$primary_url" "$output_file" 60; then
        return 0
    fi

    local primary_err="$(get_download_error)"
    rm -f "$output_file"

    if [ -n "$fallback_url" ] && [ "$fallback_url" != "$primary_url" ]; then
        echo -e "${yellow}主源下载失败: ${primary_err}${plain}"
        echo -e "正在切换备用下载节点: ${fallback_url}"
        if download_file "$fallback_url" "$output_file" 60; then
            return 0
        fi
        local fallback_err="$(get_download_error)"
        rm -f "$output_file"
        set_download_error "主源失败 (${primary_err}); 备用源失败 (${fallback_err})"
        err "备用源下载同样失败: ${fallback_err}"
    else
        set_download_error "主源下载失败: ${primary_err}"
        err "主源下载失败: ${primary_err}"
    fi

    return 1
}

geo_check() {
    api_list="https://blog.cloudflare.com/cdn-cgi/trace https://dash.cloudflare.com/cdn-cgi/trace https://cf-ns.com/cdn-cgi/trace"
    ua="Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/134.0.0.0 Safari/537.36"
    set -- $api_list
    for url in $api_list; do
        text=""
        if command -v curl >/dev/null 2>&1; then
            text="$(curl -A "$ua" -m 10 -s "$url" 2>/dev/null)"
        elif command -v wget >/dev/null 2>&1; then
            text="$(wget -qO- -T 10 -U "$ua" "$url" 2>/dev/null || wget -qO- -T 10 "$url" 2>/dev/null)"
        fi
        endpoint="$(echo "$text" | sed -n 's/.*h=\([^ ]*\).*/\1/p')"
        if echo "$text" | grep -qw 'CN'; then
            isCN=true
            break
        elif echo "$url" | grep -q "$endpoint"; then
            break
        fi
    done
}

pre_check() {
    ## os_arch
    mach=$(uname -m)
    case "$mach" in
        amd64|x86_64)
            os_arch="amd64"
            ;;
        i386|i686)
            os_arch="386"
            ;;
        aarch64|arm64)
            os_arch="arm64"
            ;;
        *arm*)
            os_arch="arm"
            ;;
        s390x)
            os_arch="s390x"
            ;;
        riscv64)
            os_arch="riscv64"
            ;;
        mips)
            os_arch="mips"
            ;;
        mipsel|mipsle)
            os_arch="mipsle"
            ;;
        *)
            err "Unknown architecture: $uname"
            exit 1
            ;;
    esac

    system=$(uname)
    case "$system" in
        *Linux*)
            os="linux"
            # 检测是否为Alpine Linux
            if [ -f /etc/alpine-release ] || grep -qi "alpine" /etc/os-release 2>/dev/null; then
                os_alpine=1
                echo "检测到Alpine Linux系统"
            else
                os_alpine=0
            fi
            ;;
        *Darwin*)
            os="darwin"
            os_alpine=0
            os_macos=1
            echo "检测到macOS系统"
            ;;
        *FreeBSD*)
            os="freebsd"
            os_alpine=0
            os_macos=0
            ;;
        *)
            err "Unknown architecture: $system"
            exit 1
            ;;
    esac

    ## China_IP
    if [ -z "$CN" ]; then
        geo_check
        if [ ! -z "$isCN" ]; then
            echo "根据geoip api提供的信息，当前IP可能在中国"
            printf "是否选用中国镜像完成安装? [Y/n] :"
            read -r input
            case $input in
            [yY][eE][sS] | [yY])
                echo "使用中国镜像"
                CN=true
                ;;

            [nN][oO] | [nN])
                echo "不使用中国镜像"
                ;;
            *)
                echo "使用中国镜像"
                CN=true
                ;;
            esac
        fi
    fi

    if [ -z "$CN" ]; then
        GITHUB_RAW_URL="raw.githubusercontent.com/xos/serverstatus/master"
        GITHUB_URL="github.com"
    else
        GITHUB_RAW_URL="gitee.com/ten/ServerStatus/raw/master"
        GITHUB_URL="${R2_URL_NO_PROTO:-assets.cnic.eu.org}"
    fi
}

confirm() {
    if [ $# -gt 1 ]; then
        echo && read -r -p "$1 [默认$2]: " temp
        if [ -z "${temp}" ]; then
            temp=$2
        fi
    else
        read -r -p "$1 [y/n]: " temp
    fi
    if [ "${temp}" = "y" ] || [ "${temp}" = "Y" ]; then
        return 0
    else
        return 1
    fi
}

update_script() {
    echo -e "> 更新脚本"

    install_base || return 1

    local tmp_script="/tmp/server-status.sh"
    rm -f "$tmp_script"

    local urls=""
    if [ -n "$CN" ]; then
        urls="https://fastly.jsdelivr.net/gh/xos/serverstatus@master/script/server-status.sh https://gitee.com/ten/ServerStatus/raw/master/script/server-status.sh https://raw.githubusercontent.com/xos/serverstatus/master/script/server-status.sh"
    else
        urls="https://raw.githubusercontent.com/xos/serverstatus/master/script/server-status.sh https://fastly.jsdelivr.net/gh/xos/serverstatus@master/script/server-status.sh https://gitee.com/ten/ServerStatus/raw/master/script/server-status.sh"
    fi

    local success=0
    for url in $urls; do
        echo -e "正在从节点获取脚本: ${url}"
        if download_file "$url" "$tmp_script" 20; then
            if [ -s "$tmp_script" ] && grep -q "VERSION=" "$tmp_script" 2>/dev/null; then
                success=1
                break
            else
                local preview
                preview=$(head -n 2 "$tmp_script" 2>/dev/null | tr '\n' ' ' | sed 's/  */ /g')
                echo -e "${yellow}[警告] 节点响应内容非有效脚本 (可能被网页重定向拦截): ${preview:-空文件}${plain}"
                rm -f "$tmp_script"
            fi
        else
            echo -e "${yellow}[失败] 节点拉取失败: $(get_download_error)${plain}"
        fi
    done

    if [ "$success" -ne 1 ]; then
        err "脚本更新失败！所有候选源均无法获取有效脚本。"
        err "已尝试的所有下载节点:"
        for u in $urls; do
            err "  - $u"
        done
        err "最后一次错误详情: $(get_download_error)"
        err "请检查本机的网络连接、DNS 解析或防火墙规则。"
        if [ $# = 0 ]; then
            before_show_menu
        fi
        return 1
    fi

    local new_version
    new_version=$(grep "VERSION=" "$tmp_script" | head -n 1 | awk -F "=" '{print $2}' | sed 's/\"//g;s/,//g;s/ //g')
    echo -e "获取成功！最新版本为: ${green}${new_version:-未知}${plain}"
    local target_script="$0"
    if [ ! -f "$target_script" ] || [ ! -w "$target_script" ]; then
        target_script="./server-status.sh"
    fi
    mv -f "$tmp_script" "$target_script" && chmod a+x "$target_script"

    echo -e "3s后执行新脚本..."
    sleep 3s
    clear
    exec "$target_script"
    exit 0
}

before_show_menu() {
    echo && echo -n -e "${yellow}* 按回车返回主菜单 *${plain}" && read temp
    show_menu
}

install_base() {
    if [ "$os_alpine" = 1 ]; then
        # 1. 证书：仅当系统缺失证书库时安装 ca-certificates (~700KB)
        if [ ! -f /etc/ssl/certs/ca-certificates.crt ] && ! command -v update-ca-certificates >/dev/null 2>&1; then
            echo -e "${yellow}检测到未安装 ca-certificates，正在安装以支持 HTTPS 下载...${plain}"
            install_soft ca-certificates
            command -v update-ca-certificates >/dev/null 2>&1 && sudo update-ca-certificates >/dev/null 2>&1 || true
        fi
        # 2. 解压工具：BusyBox 自带 unzip，仅当系统完全没有 unzip 时才安装
        if ! command -v unzip >/dev/null 2>&1; then
            install_soft unzip
        fi
        # 3. 下载工具：只要已有 curl 或 wget 之一即可，避免额外安装体积庞大的下载器
        if ! command -v curl >/dev/null 2>&1 && ! command -v wget >/dev/null 2>&1; then
            install_soft curl
        fi
        # 4. OpenRC：仅当精简系统缺失时才安装
        if ! command -v rc-service >/dev/null 2>&1 || ! command -v rc-update >/dev/null 2>&1; then
            echo -e "${yellow}检测到未安装 OpenRC，正在安装...${plain}"
            install_soft openrc
        fi
        init_openrc_env
    else
        ensure_commands curl wget unzip || return 1
    fi
}

# 确保依赖命令存在，不存在则尝试安装并做二次校验
ensure_commands() {
    local cmd
    local missing_cmds=""

    for cmd in "$@"; do
        if [ "$cmd" = "ca-certificates" ]; then
            if [ ! -f /etc/ssl/certs/ca-certificates.crt ] && ! command -v update-ca-certificates >/dev/null 2>&1; then
                missing_cmds="${missing_cmds:+$missing_cmds }$cmd"
            fi
        elif ! command -v "$cmd" >/dev/null 2>&1; then
            missing_cmds="${missing_cmds:+$missing_cmds }$cmd"
        fi
    done

    if [ -n "$missing_cmds" ]; then
        echo -e "${yellow}检测到缺少依赖: ${missing_cmds}，尝试自动安装...${plain}"
        install_soft $missing_cmds
        if echo " $missing_cmds " | grep -q " ca-certificates "; then
            command -v update-ca-certificates >/dev/null 2>&1 && sudo update-ca-certificates >/dev/null 2>&1 || true
        fi
    fi

    missing_cmds=""
    for cmd in "$@"; do
        if [ "$cmd" = "ca-certificates" ]; then
            if [ ! -f /etc/ssl/certs/ca-certificates.crt ] && ! command -v update-ca-certificates >/dev/null 2>&1; then
                missing_cmds="${missing_cmds:+$missing_cmds }$cmd"
            fi
        elif ! command -v "$cmd" >/dev/null 2>&1; then
            missing_cmds="${missing_cmds:+$missing_cmds }$cmd"
        fi
    done

    if [ -n "$missing_cmds" ]; then
        err "缺少必要依赖: ${missing_cmds}，请先安装后重试"
        return 1
    fi

    return 0
}

install_soft() {
	# 根据不同系统使用相应的包管理器
    if [ "$os_alpine" = 1 ]; then
        # Alpine Linux 优先使用 --no-cache 安装，避免在磁盘留下数十兆索引缓存
        if ! sudo apk add --no-cache "$@"; then
            echo -e "${yellow}apk 直接安装失败，尝试更新源索引后重试...${plain}"
            sudo apk update && sudo apk add "$@"
        fi
    elif [ "$os_macos" = 1 ]; then
        # macOS 使用 Homebrew
        if command -v brew >/dev/null 2>&1; then
            brew install "$@"
        else
            echo -e "${yellow}未检测到Homebrew，正在安装...${plain}"
            /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
            brew install "$@"
        fi
    elif command -v yum >/dev/null 2>&1; then
        # RHEL/CentOS/Fedora 使用 yum
        sudo yum makecache && sudo yum install "$@" selinux-policy -y
    elif command -v apt >/dev/null 2>&1; then
        # Debian/Ubuntu 使用 apt
        if ! sudo apt install -y "$@"; then
            echo -e "${yellow}apt 直接安装失败，尝试刷新软件源后重试...${plain}"
            if ! sudo apt update; then
                echo -e "${yellow}apt update 失败，尝试忽略失效源继续更新索引...${plain}"
                sudo apt -o Acquire::AllowInsecureRepositories=true -o Acquire::AllowDowngradeToInsecureRepositories=true update || true
            fi
            sudo apt install -y --fix-missing "$@"
        fi
    elif command -v pacman >/dev/null 2>&1; then
        # Arch Linux 使用 pacman
        sudo pacman -Syu "$@" base-devel --noconfirm && install_arch
    elif command -v apt-get >/dev/null 2>&1; then
        # 旧版 Debian/Ubuntu 使用 apt-get
        if ! sudo apt-get install -y "$@"; then
            echo -e "${yellow}apt-get 直接安装失败，尝试刷新软件源后重试...${plain}"
            if ! sudo apt-get update; then
                echo -e "${yellow}apt-get update 失败，尝试忽略失效源继续更新索引...${plain}"
                sudo apt-get -o Acquire::AllowInsecureRepositories=true -o Acquire::AllowDowngradeToInsecureRepositories=true update || true
            fi
            sudo apt-get install -y --fix-missing "$@"
        fi
    else
        echo -e "${red}未找到支持的包管理器${plain}"
        exit 1
    fi
}

selinux() {
    # Alpine Linux 和 macOS 不使用SELinux，跳过处理
    if [ "$os_alpine" = 1 ] || [ "$os_macos" = 1 ]; then
        return 0
    fi

    #判断当前的状态
    command -v getenforce >/dev/null 2>&1
    if [ $? -eq 0 ]; then
        getenforce | grep '[Ee]nfor'
        if [ $? -eq 0 ]; then
            echo "SELinux是开启状态，正在关闭！"
            sudo setenforce 0 &>/dev/null
            find_key="SELINUX="
            sudo sed -ri "/^$find_key/c${find_key}disabled" /etc/selinux/config
        fi
    fi
}

setup_agent_config_template() {
    [ -d "$AGENT_PATH" ] || sudo mkdir -p "$AGENT_PATH"

    if [ -f "$AGENT_CONFIG" ]; then
        return 0
    fi

    echo "正在下载配置文件模板..."
    if download_file "https://${GITHUB_RAW_URL}/script/config.yml" "$AGENT_CONFIG" 15; then
        return 0
    fi

    local dl_err="$(get_download_error)"
    [ -n "$dl_err" ] && echo -e "${yellow}远程模板下载失败 (${dl_err})，尝试本地文件或内置模板...${plain}"

    if [ -f "./script/config.yml" ]; then
        cp "./script/config.yml" "$AGENT_CONFIG" && return 0
    elif [ -f "../script/config.yml" ]; then
        cp "../script/config.yml" "$AGENT_CONFIG" && return 0
    fi

    echo -e "${yellow}未找到远程及本地模板，生成默认配置文件...${plain}"
    cat <<'EOF' > "$AGENT_CONFIG"
# ServerAgent 配置文件
server: ""
clientSecret: ""
tls: false
insecureTLS: false
debug: false
gpu: false
temperature: false
disableAutoUpdate: false
disableForceUpdate: false
disableCommandExecute: false
disableNat: false
disableSendQuery: false
skipConnectionCount: false
skipProcsCount: false
reportDelay: 1
ipReportPeriod: 1800
useIPv6CountryCode: false
useR2ToUpgrade: false
EOF
    return 0
}

setup_openrc_service() {
    echo "正在配置 OpenRC 服务..."
    init_openrc_env
    if download_file "https://${GITHUB_RAW_URL}/script/server-agent.openrc" "$AGENT_OPENRC_SERVICE" 15; then
        chmod +x "$AGENT_OPENRC_SERVICE"
        return 0
    fi

    local dl_err="$(get_download_error)"
    [ -n "$dl_err" ] && echo -e "${yellow}远程 OpenRC 脚本下载失败 (${dl_err})，尝试本地文件或内置模板...${plain}"

    if [ -f "./script/server-agent.openrc" ]; then
        cp "./script/server-agent.openrc" "$AGENT_OPENRC_SERVICE"
        chmod +x "$AGENT_OPENRC_SERVICE"
        return 0
    elif [ -f "../script/server-agent.openrc" ]; then
        cp "../script/server-agent.openrc" "$AGENT_OPENRC_SERVICE"
        chmod +x "$AGENT_OPENRC_SERVICE"
        return 0
    fi

    echo -e "${yellow}未能从远程或本地获取 OpenRC 服务文件，正在生成本地服务脚本...${plain}"
    cat <<'EOF' > "$AGENT_OPENRC_SERVICE"
#!/sbin/openrc-run

name="server-agent"
description="ServerStatus Agent Service"
command="/opt/server-status/agent/server-agent"
directory="/opt/server-status/agent"
start_stop_daemon_args="--chdir /opt/server-status/agent"
command_user="root"
command_background="yes"
pidfile="/run/${name}.pid"

output_log="/var/log/${name}.log"
error_log="/var/log/${name}.log"

depend() {
    need net
    after firewall
}

start_pre() {
    if [ ! -x "${command}" ]; then
        eerror "ServerStatus Agent executable not found: ${command}"
        return 1
    fi
    if [ ! -f "/opt/server-status/agent/config.yml" ]; then
        eerror "ServerStatus Agent config file not found: /opt/server-status/agent/config.yml"
        return 1
    fi
    checkpath --directory --owner root:root --mode 0755 /var/log
    checkpath --file --owner root:root --mode 0644 /var/log/${name}.log
    checkpath --directory --owner root:root --mode 0755 /run

    if [ -s "/var/log/${name}_error.log" ]; then
        if [ ! -s "/var/log/${name}.log" ]; then
            cat "/var/log/${name}_error.log" >> "/var/log/${name}.log" 2>/dev/null || true
        fi
        rm -f "/var/log/${name}_error.log" 2>/dev/null || true
    fi
    return 0
}

start_post() {
    sleep 1
    if [ -f "${pidfile}" ]; then
        local pid=$(cat "${pidfile}")
        if kill -0 "${pid}" 2>/dev/null; then
            einfo "ServerStatus Agent started successfully with PID ${pid}"
            return 0
        else
            eerror "ServerStatus Agent failed to start properly"
            return 1
        fi
    else
        eerror "ServerStatus Agent PID file not created"
        return 1
    fi
}

stop_post() {
    if [ -f "${pidfile}" ]; then
        rm -f "${pidfile}"
    fi
    einfo "ServerStatus Agent stopped"
    return 0
}

status() {
    if [ -f "${pidfile}" ]; then
        local pid=$(cat "${pidfile}")
        if kill -0 "${pid}" 2>/dev/null; then
            einfo "ServerStatus Agent is running with PID ${pid}"
            return 0
        else
            eerror "ServerStatus Agent PID file exists but process is not running"
            return 1
        fi
    else
        einfo "ServerStatus Agent is not running"
        return 1
    fi
}
EOF
    chmod +x "$AGENT_OPENRC_SERVICE"
    return 0
}

install_agent() {
    install_base || return 1
    selinux

    echo -e "> 安装探针"

    echo -e "正在获取探针版本号"

    local version=$(get_agent_version)

    if [ -z "$version" ]; then
        err "获取探针版本号失败！"
        local dl_err="$(get_download_error)"
        [ -n "$dl_err" ] && err "错误详情: ${dl_err}"
        if [ $# = 0 ]; then
            before_show_menu
        fi
        return 1
    else
        echo -e "当前最新版本为: ${version}"
    fi

    # 探针文件夹
    if [ -n "${AGENT_PATH}" ]; then
        # macOS下可能需要sudo权限创建/opt目录
        if [ "$os_macos" = 1 ]; then
            if [ ! -d "/opt" ]; then
                echo "创建/opt目录需要管理员权限..."
                sudo mkdir -p /opt 2>/dev/null || {
                    echo "无法创建/opt目录，请手动执行: sudo mkdir -p /opt"
                    exit 1
                }
            fi
            if [ ! -w "/opt" ]; then
                echo "设置目录权限需要管理员权限..."
                sudo mkdir -p $AGENT_PATH
                sudo chown $(whoami):staff $AGENT_PATH
                sudo chmod 755 $AGENT_PATH
            else
                mkdir -p $AGENT_PATH
                chmod 755 $AGENT_PATH
            fi
        else
            mkdir -p $AGENT_PATH
            chmod 777 -R $AGENT_PATH
        fi
    fi
    echo "正在下载监控端"

    # 根据系统选择相应的二进制文件
    if [ "$os_macos" = 1 ]; then
        if [ -z "$CN" ]; then
            AGENT_URL="https://${GITHUB_URL}/xos/serveragent/releases/download/${version}/server-agent_darwin_${os_arch}.zip"
            AGENT_FALLBACK_URL=""
        else
            AGENT_URL="https://${GITHUB_URL}/serveragent/${version}/server-agent_darwin_${os_arch}.zip"
            AGENT_FALLBACK_URL="https://github.com/xos/serveragent/releases/download/${version}/server-agent_darwin_${os_arch}.zip"
        fi
        AGENT_ZIP="server-agent_darwin_${os_arch}.zip"
    else
        if [ -z "$CN" ]; then
            AGENT_URL="https://${GITHUB_URL}/xos/serveragent/releases/download/${version}/server-agent_linux_${os_arch}.zip"
            AGENT_FALLBACK_URL=""
        else
            AGENT_URL="https://${GITHUB_URL}/serveragent/${version}/server-agent_linux_${os_arch}.zip"
            AGENT_FALLBACK_URL="https://github.com/xos/serveragent/releases/download/${version}/server-agent_linux_${os_arch}.zip"
        fi
        AGENT_ZIP="server-agent_linux_${os_arch}.zip"
    fi

    echo -e "正在下载探针"
    if ! download_release_archive "$AGENT_URL" "$AGENT_FALLBACK_URL" "$AGENT_ZIP"; then
        err "探针压缩包下载失败，请检查中国镜像或 GitHub 的网络连接"
        local dl_err="$(get_download_error)"
        [ -n "$dl_err" ] && err "错误详情: ${dl_err}"
        if [ $# = 0 ]; then
            before_show_menu
        fi
        return 1
    fi
    if ! unzip -qo "$AGENT_ZIP"; then
        err "解压探针压缩包 ($AGENT_ZIP) 失败！"
        rm -f "$AGENT_ZIP"
        if [ $# = 0 ]; then
            before_show_menu
        fi
        return 1
    fi
    chmod +x server-agent 2>/dev/null || true
    mv -f server-agent "$AGENT_PATH/" &&
        rm -rf "$AGENT_ZIP" README.md

    if [ ! -f "$AGENT_PATH/server-agent" ]; then
        err "未找到探针程序: $AGENT_PATH/server-agent，安装未完成"
        if [ $# = 0 ]; then
            before_show_menu
        fi
        return 1
    fi

    # macOS下设置正确的文件权限
    if [ "$os_macos" = 1 ]; then
        echo "设置文件权限..."
        # 确保当前用户拥有文件权限
        if [ -f "$AGENT_PATH/server-agent" ]; then
            # 检查并修复所有者
            if [ "$(stat -f '%u' $AGENT_PATH/server-agent)" != "$(id -u)" ]; then
                echo "修复探针程序所有者..."
                sudo chown $(whoami):staff "$AGENT_PATH/server-agent" 2>/dev/null || true
            fi
            # 设置执行权限
            chmod 755 "$AGENT_PATH/server-agent" 2>/dev/null || true
            echo "探针程序权限设置完成"
        fi

        # 确保整个目录的权限正确
        if [ -d "$AGENT_PATH" ]; then
            if [ "$(stat -f '%u' $AGENT_PATH)" != "$(id -u)" ]; then
                echo "修复目录所有者..."
                sudo chown -R $(whoami):staff "$AGENT_PATH" 2>/dev/null || true
            fi
            chmod 755 "$AGENT_PATH" 2>/dev/null || true
        fi
    fi

    # 下载配置文件模板
    setup_agent_config_template

    # macOS下设置配置文件权限
    if [ "$os_macos" = 1 ]; then
        if [ -f "$AGENT_CONFIG" ]; then
            if [ "$(stat -f '%u' $AGENT_CONFIG)" != "$(id -u)" ]; then
                sudo chown $(whoami):staff "$AGENT_CONFIG" 2>/dev/null || true
            fi
            chmod 644 "$AGENT_CONFIG" 2>/dev/null || true
            echo "配置文件权限设置完成"
        fi
    fi

    # 验证配置文件
    if [ -f "$AGENT_CONFIG" ]; then
        echo "验证配置文件..."
        # 检查配置文件是否包含必要的字段
        if grep -q "server:" "$AGENT_CONFIG" && grep -q "clientSecret:" "$AGENT_CONFIG"; then
            echo -e "${green}配置文件验证通过${plain}"
        else
            echo -e "${yellow}配置文件可能不完整，请检查${plain}"
            echo "配置文件内容预览:"
            head -10 "$AGENT_CONFIG" 2>/dev/null || echo "无法读取配置文件"
        fi
    fi

    # 根据系统类型配置相应的服务文件
    if [ "$os_alpine" = 1 ]; then
        setup_openrc_service || return 1
    elif [ "$os_macos" = 1 ]; then
        echo "正在下载LaunchAgent配置文件"
        # 确保LaunchAgents目录存在
        mkdir -p "$HOME/Library/LaunchAgents"
        if ! download_file "https://${GITHUB_RAW_URL}/script/com.serverstatus.agent.plist" "$AGENT_LAUNCHD_SERVICE" 10; then
            err "LaunchAgent配置文件下载失败，请检查本机能否连接 ${GITHUB_RAW_URL}"
            local dl_err="$(get_download_error)"
            [ -n "$dl_err" ] && err "错误详情: ${dl_err}"
            if [ $# = 0 ]; then
                before_show_menu
            fi
            return 1
        fi
    else
        # 其他系统使用systemd
        echo "正在下载systemd服务文件"
        if ! download_file "https://${GITHUB_RAW_URL}/script/server-agent.service" "$AGENT_SERVICE" 10; then
            err "Service文件下载失败，请检查本机能否连接 ${GITHUB_RAW_URL}"
            local dl_err="$(get_download_error)"
            [ -n "$dl_err" ] && err "错误详情: ${dl_err}"
            if [ $# = 0 ]; then
                before_show_menu
            fi
            return 1
        fi
    fi

    if [ $# -ge 3 ]; then
        modify_agent_config "$@"
    else
        modify_agent_config 0
    fi

    if [ $# = 0 ]; then
        before_show_menu
    fi
}

update_agent() {
    echo -e "> 更新 探针"

    install_base || return 1

    echo -e "正在获取探针版本号"

    local version=$(get_agent_version)

    if [ -z "$version" ]; then
        err "获取探针版本号失败！"
        local dl_err="$(get_download_error)"
        [ -n "$dl_err" ] && err "错误详情: ${dl_err}"
        if [ $# = 0 ]; then
            before_show_menu
        fi
        return 1
    else
        echo -e "当前最新版本为: ${version}"
    fi

    # 探针文件夹
    if [ -n "${AGENT_PATH}" ]; then
        # macOS下可能需要sudo权限创建/opt目录
        if [ "$os_macos" = 1 ]; then
            if [ ! -d "/opt" ]; then
                echo "创建/opt目录需要管理员权限..."
                sudo mkdir -p /opt 2>/dev/null || {
                    echo "无法创建/opt目录，请手动执行: sudo mkdir -p /opt"
                    exit 1
                }
            fi
            if [ ! -w "/opt" ]; then
                echo "设置目录权限需要管理员权限..."
                sudo mkdir -p $AGENT_PATH
                sudo chown $(whoami):staff $AGENT_PATH
                sudo chmod 755 $AGENT_PATH
            else
                mkdir -p $AGENT_PATH
                chmod 755 $AGENT_PATH
            fi
        else
            mkdir -p $AGENT_PATH
            chmod 777 -R $AGENT_PATH
        fi
    fi

    echo "正在下载探针端"

    # 根据系统选择相应的二进制文件
    if [ "$os_macos" = 1 ]; then
        if [ -z "$CN" ]; then
            AGENT_URL="https://${GITHUB_URL}/xos/serveragent/releases/download/${version}/server-agent_darwin_${os_arch}.zip"
            AGENT_FALLBACK_URL=""
        else
            AGENT_URL="https://${GITHUB_URL}/serveragent/${version}/server-agent_darwin_${os_arch}.zip"
            AGENT_FALLBACK_URL="https://github.com/xos/serveragent/releases/download/${version}/server-agent_darwin_${os_arch}.zip"
        fi
        AGENT_ZIP="server-agent_darwin_${os_arch}.zip"
    else
        if [ -z "$CN" ]; then
            AGENT_URL="https://${GITHUB_URL}/xos/serveragent/releases/download/${version}/server-agent_linux_${os_arch}.zip"
            AGENT_FALLBACK_URL=""
        else
            AGENT_URL="https://${GITHUB_URL}/serveragent/${version}/server-agent_linux_${os_arch}.zip"
            AGENT_FALLBACK_URL="https://github.com/xos/serveragent/releases/download/${version}/server-agent_linux_${os_arch}.zip"
        fi
        AGENT_ZIP="server-agent_linux_${os_arch}.zip"
    fi

    echo -e "正在下载探针"
    if ! download_release_archive "$AGENT_URL" "$AGENT_FALLBACK_URL" "$AGENT_ZIP"; then
        err "探针压缩包下载失败，请检查中国镜像或 GitHub 的网络连接"
        local dl_err="$(get_download_error)"
        [ -n "$dl_err" ] && err "错误详情: ${dl_err}"
        if [ $# = 0 ]; then
            before_show_menu
        fi
        return 1
    fi
    if ! unzip -qo "$AGENT_ZIP"; then
        err "解压探针压缩包 ($AGENT_ZIP) 失败！"
        rm -f "$AGENT_ZIP"
        if [ $# = 0 ]; then
            before_show_menu
        fi
        return 1
    fi
    chmod +x server-agent 2>/dev/null || true
    mv -f server-agent "$AGENT_PATH/" &&
        rm -rf "$AGENT_ZIP" README.md

    if [ ! -f "$AGENT_PATH/server-agent" ]; then
        err "未找到探针程序: $AGENT_PATH/server-agent，更新未完成"
        if [ $# = 0 ]; then
            before_show_menu
        fi
        return 1
    fi

    # 检查配置文件是否存在，如果不存在则下载/生成
    if [ ! -f "${AGENT_CONFIG}" ]; then
        setup_agent_config_template
    fi

    if [ "$os_alpine" = 1 ]; then
        [ -f "$AGENT_OPENRC_SERVICE" ] || setup_openrc_service
    fi

    service_restart

    if [ $# = 0 ]; then
        echo -e "更新完毕！"
        before_show_menu
    fi
}

set_host(){
    read -r -p "请输入一个解析到探针面板所在IP的域名: " grpc_host
    [ -z "${grpc_host}" ] && echo "已取消输入..." && exit 1
}
set_port(){
    read -r -p "请输入探针面板 GRPC 端口（默认：2222）: " grpc_port
    [ -z "${grpc_port}" ] && grpc_port=2222
}
set_secret(){
    read -r -p "请输入探针密钥: " client_secret
    [ -z "${client_secret}" ] && echo "已取消输入..." && exit 1
}
# 更新配置文件中的值
update_config_value() {
    local key=$1
    local value=$2
    local config_file=$3

    [ -f "$config_file" ] || touch "$config_file"

    local formatted=""
    case "$value" in
        true|false)
            formatted="${key}: ${value}"
            ;;
        ""|*[!0-9]*)
            formatted="${key}: \"${value}\""
            ;;
        *)
            formatted="${key}: ${value}"
            ;;
    esac

    if grep -q "^${key}:" "$config_file" 2>/dev/null; then
        if [ "$os_macos" = 1 ]; then
            sed -i '' "s|^${key}:.*|${formatted}|" "$config_file"
        else
            sed -i "s|^${key}:.*|${formatted}|" "$config_file"
        fi
    else
        echo "$formatted" >> "$config_file"
    fi
}

read_config(){
	[[ ! -e ${AGENT_CONFIG} ]] && echo -e "${red} 探针配置文件不存在 ! ${plain}" && exit 1
    	host=$(grep '^server:' ${AGENT_CONFIG} | awk '{print $2}' | sed 's/"//g' | sed 's/\:/ /' | awk '{print $1}')
	port=$(grep '^server:' ${AGENT_CONFIG} | awk '{print $2}' | sed 's/"//g' | sed 's/\:/ /' | awk '{print $2}')
	secret=$(grep '^clientSecret:' ${AGENT_CONFIG} | awk '{print $2}' | sed 's/"//g')
}
set_agent(){
    echo && echo -e "修改探针配置
    =========================
    ${green}1.${plain}  修改 域名
    ${green}2.${plain}  修改 端口
    ${green}3.${plain}  修改 密钥
    =========================
    ${green}4.${plain}  修改 全部配置
    ${green}5.${plain}  高级配置选项
    ${green}6.${plain}  编辑配置文件" && echo
	    read -r -p "(默认: 取消): " modify
        [ -z "${modify}" ] && echo "已取消..." && exit 1

	if [[ "${modify}" == "1" ]]; then
        read_config
		set_host
        grpc_host=${grpc_host}
        # 修改配置文件中的server地址
        update_config_value "server" "${grpc_host}:${port}" ${AGENT_CONFIG}
        echo -e "探针域名 ${green}修改成功，请稍等探针重启生效${plain}"
        daemon_reload
        service_enable
        service_restart
        echo -e "探针 已重启完毕！"
        before_show_menu

	elif [[ "${modify}" == "2" ]]; then
        read_config
		set_port
        grpc_port=${grpc_port}
        # 修改配置文件中的server端口
        update_config_value "server" "${host}:${grpc_port}" ${AGENT_CONFIG}
        echo -e "探针端口${green} 修改成功，请稍等探针重启生效${plain}"
        daemon_reload
        service_enable
        service_restart
        echo -e "探针 已重启完毕！"
        before_show_menu

	elif [[ "${modify}" == "3" ]]; then
        read_config
		set_secret
        client_secret=${client_secret}
        # 修改配置文件中的clientSecret
        update_config_value "clientSecret" "${client_secret}" ${AGENT_CONFIG}
        echo -e "探针密钥${green} 修改成功，请稍等探针重启生效${plain}"
        daemon_reload
        service_enable
        service_restart
        echo -e "探针 已重启完毕！"
        before_show_menu

	elif [[ "${modify}" == "4" ]]; then
		modify_agent_config
	elif [[ "${modify}" == "5" ]]; then
		advanced_config_menu
	elif [[ "${modify}" == "6" ]]; then
		edit_config_file
    else
		echo -e "${Error} 请输入正确的数字(1-6)" && exit 1
    fi
    sleep 3s
    start_menu
}

# 高级配置菜单
advanced_config_menu() {
    echo && echo -e "高级配置选项
    =========================
    ${green}1.${plain}  启用/禁用 TLS 加密
    ${green}2.${plain}  启用/禁用 调试模式
    ${green}3.${plain}  启用/禁用 GPU 监控
    ${green}4.${plain}  启用/禁用 温度监控
    ${green}5.${plain}  启用/禁用 自动更新
    ${green}6.${plain}  启用/禁用 命令执行
    ${green}7.${plain}  启用/禁用 内网穿透
    ${green}8.${plain}  设置 上报间隔
    =========================
    ${green}0.${plain}  返回上级菜单" && echo
    read -r -p "请选择配置项 [0-8]: " advanced_option

    case "${advanced_option}" in
    1)
        toggle_config_boolean "tls" "TLS 加密"
        ;;
    2)
        toggle_config_boolean "debug" "调试模式"
        ;;
    3)
        toggle_config_boolean "gpu" "GPU 监控"
        ;;
    4)
        toggle_config_boolean "temperature" "温度监控"
        ;;
    5)
        toggle_config_boolean "disableAutoUpdate" "禁用自动更新"
        ;;
    6)
        toggle_config_boolean "disableCommandExecute" "禁用命令执行"
        ;;
    7)
        toggle_config_boolean "disableNat" "禁用内网穿透"
        ;;
    8)
        set_report_delay
        ;;
    0)
        set_agent
        ;;
    *)
        echo -e "${red}请输入正确的数字 [0-8]${plain}"
        advanced_config_menu
        ;;
    esac
}

# 切换布尔配置项
toggle_config_boolean() {
    local key=$1
    local description=$2

    [[ ! -e ${AGENT_CONFIG} ]] && echo -e "${red} 探针配置文件不存在 ! ${plain}" && exit 1

    current_value=$(grep "^${key}:" ${AGENT_CONFIG} | awk '{print $2}')

    echo "当前 ${description} 状态: ${current_value}"
    read -r -p "是否切换状态? [y/n]: " toggle

    if [[ x"${toggle}" == x"y" || x"${toggle}" == x"Y" ]]; then
        if [[ "${current_value}" == "true" ]]; then
            new_value="false"
        else
            new_value="true"
        fi

        update_config_value "${key}" "${new_value}" ${AGENT_CONFIG}
        echo -e "${description} ${green}已设置为 ${new_value}${plain}"

        echo "重启探针以使配置生效..."
        service_restart
        echo -e "探针 已重启完毕！"
    fi

    advanced_config_menu
}

# 设置上报间隔
set_report_delay() {
    [[ ! -e ${AGENT_CONFIG} ]] && echo -e "${red} 探针配置文件不存在 ! ${plain}" && exit 1

    current_delay=$(grep "^reportDelay:" ${AGENT_CONFIG} | awk '{print $2}')
    echo "当前上报间隔: ${current_delay} 秒"

    read -r -p "请输入新的上报间隔 (1-4秒，推荐1秒): " new_delay

    if [[ "${new_delay}" =~ ^[1-4]$ ]]; then
        update_config_value "reportDelay" "${new_delay}" ${AGENT_CONFIG}
        echo -e "上报间隔 ${green}已设置为 ${new_delay} 秒${plain}"

        echo "重启探针以使配置生效..."
        service_restart
        echo -e "探针 已重启完毕！"
    else
        echo -e "${red}无效的上报间隔，请输入1-4之间的数字${plain}"
    fi

    advanced_config_menu
}

# 编辑配置文件
edit_config_file() {
    [[ ! -e ${AGENT_CONFIG} ]] && echo -e "${red} 探针配置文件不存在 ! ${plain}" && exit 1

    echo -e "即将打开配置文件进行编辑: ${AGENT_CONFIG}"
    echo -e "${yellow}注意: 修改配置文件后需要重启探针才能生效${plain}"
    echo -e "按回车键继续..."
    read

    # 尝试使用不同的编辑器
    if command -v nano >/dev/null 2>&1; then
        nano ${AGENT_CONFIG}
    elif command -v vim >/dev/null 2>&1; then
        vim ${AGENT_CONFIG}
    elif command -v vi >/dev/null 2>&1; then
        vi ${AGENT_CONFIG}
    else
        echo -e "${red}未找到可用的文本编辑器 (nano/vim/vi)${plain}"
        echo -e "配置文件位置: ${AGENT_CONFIG}"
        echo -e "您可以手动编辑此文件"
        before_show_menu
        return 1
    fi

    echo -e "配置文件编辑完成"
    read -r -p "是否重启探针以使配置生效? [y/n]: " restart_choice

    if [[ x"${restart_choice}" == x"y" || x"${restart_choice}" == x"Y" ]]; then
        echo "重启探针中..."
        service_restart
        echo -e "探针 已重启完毕！"
    fi

    before_show_menu
}

modify_agent_config() {
    echo -e "> 初始化探针配置"

    # 根据系统类型配置相应的服务文件
    if [ "$os_alpine" = 1 ]; then
        # Alpine使用OpenRC
        [ -f "$AGENT_OPENRC_SERVICE" ] || setup_openrc_service
    elif [ "$os_macos" = 1 ]; then
        # macOS使用LaunchAgent
        if [ ! -f "$AGENT_LAUNCHD_SERVICE" ]; then
            mkdir -p "$HOME/Library/LaunchAgents"
            if ! download_file "https://${GITHUB_RAW_URL}/script/com.serverstatus.agent.plist" "$AGENT_LAUNCHD_SERVICE" 10; then
                err "LaunchAgent配置文件下载失败，请检查本机能否连接 ${GITHUB_RAW_URL}"
                local dl_err="$(get_download_error)"
                [ -n "$dl_err" ] && err "错误详情: ${dl_err}"
                if [ $# = 0 ]; then
                    before_show_menu
                fi
                return 1
            fi
        fi
    else
        # 其他系统使用systemd
        if [ ! -f "$AGENT_SERVICE" ]; then
            if ! download_file "https://${GITHUB_RAW_URL}/script/server-agent.service" "$AGENT_SERVICE" 10; then
                err "Service文件下载失败，请检查本机能否连接 ${GITHUB_RAW_URL}"
                local dl_err="$(get_download_error)"
                [ -n "$dl_err" ] && err "错误详情: ${dl_err}"
                if [ $# = 0 ]; then
                    before_show_menu
                fi
                return 1
            fi
        fi
    fi

    # 确保配置文件存在
    if [ ! -f "${AGENT_CONFIG}" ]; then
        setup_agent_config_template
    fi

    if [[ $# -lt 3 ]]; then
        echo "请先在管理面板上添加探针服务，记录下密钥" &&
            read -r -p "请输入一个解析到探针面板所在IP的域名: " grpc_host &&
            read -r -p "请输入探针面板 GRPC 端口（默认：2222）: " grpc_port &&
            read -r -p "请输入探针密钥: " client_secret
        if [ -z "${grpc_host}" ] || [ -z "${client_secret}" ]; then
            echo -e "${red}所有选项都不能为空${plain}"
            before_show_menu
            return 1
        fi

        if [[ -z "${grpc_port}" ]]; then
            grpc_port=2222
        fi
    else
        grpc_host=$1
        grpc_port=$2
        client_secret=$3
    fi

    # 修改配置文件而不是service文件
    update_config_value "server" "${grpc_host}:${grpc_port}" ${AGENT_CONFIG}
    update_config_value "clientSecret" "${client_secret}" ${AGENT_CONFIG}

    # 处理额外的参数（如--tls等）
    shift 3
    if [ $# -gt 0 ]; then
        # 处理TLS参数
        if [[ "$*" == *"--tls"* ]]; then
            update_config_value "tls" "true" ${AGENT_CONFIG}
        fi
        # 处理insecure-tls参数
        if [[ "$*" == *"--insecure-tls"* ]]; then
            update_config_value "insecureTLS" "true" ${AGENT_CONFIG}
        fi
        # 处理debug参数
        if [[ "$*" == *"--debug"* ]]; then
            update_config_value "debug" "true" ${AGENT_CONFIG}
        fi
        # 处理disable-auto-update参数
        if [[ "$*" == *"--disable-auto-update"* ]]; then
            update_config_value "disableAutoUpdate" "true" ${AGENT_CONFIG}
        fi
        # 处理disable-force-update参数
        if [[ "$*" == *"--disable-force-update"* ]]; then
            update_config_value "disableForceUpdate" "true" ${AGENT_CONFIG}
        fi
        # 处理disable-command-execute参数
        if [[ "$*" == *"--disable-command-execute"* ]]; then
            update_config_value "disableCommandExecute" "true" ${AGENT_CONFIG}
        fi
        # 处理disable-nat参数
        if [[ "$*" == *"--disable-nat"* ]]; then
            update_config_value "disableNat" "true" ${AGENT_CONFIG}
        fi
        # 处理disable-send-query参数
        if [[ "$*" == *"--disable-send-query"* ]]; then
            update_config_value "disableSendQuery" "true" ${AGENT_CONFIG}
        fi
        # 处理skip-connection-count参数
        if [[ "$*" == *"--skip-connection-count"* ]]; then
            update_config_value "skipConnectionCount" "true" ${AGENT_CONFIG}
        fi
        # 处理skip-procs-count参数
        if [[ "$*" == *"--skip-procs-count"* ]]; then
            update_config_value "skipProcsCount" "true" ${AGENT_CONFIG}
        fi
        # 处理gpu参数
        if [[ "$*" == *"--gpu"* ]]; then
            update_config_value "gpu" "true" ${AGENT_CONFIG}
        fi
        # 处理temperature参数
        if [[ "$*" == *"--temperature"* ]]; then
            update_config_value "temperature" "true" ${AGENT_CONFIG}
        fi
        # 处理use-ipv6-country-code参数
        if [[ "$*" == *"--use-ipv6-country-code"* ]]; then
            update_config_value "useIPv6CountryCode" "true" ${AGENT_CONFIG}
        fi
        # 处理 R2 更新参数
        if [[ "$*" == *"--use-r2-to-upgrade"* ]]; then
            update_config_value "useR2ToUpgrade" "true" ${AGENT_CONFIG}
        fi
        # 处理report-delay参数
        if [[ "$*" =~ --report-delay[[:space:]]+([0-9]+) ]]; then
            update_config_value "reportDelay" "${BASH_REMATCH[1]}" ${AGENT_CONFIG}
        elif echo " $*" | grep -Eq -- '--report-delay[= ][0-9]+'; then
            report_delay_val=$(echo " $*" | sed -n 's/.*--report-delay[= ][[:space:]]*\([0-9]\{1,\}\).*/\1/p')
            [ -n "$report_delay_val" ] && update_config_value "reportDelay" "$report_delay_val" "$AGENT_CONFIG"
        fi
        # 处理ip-report-period参数
        if [[ "$*" =~ --ip-report-period[[:space:]]+([0-9]+) ]]; then
            update_config_value "ipReportPeriod" "${BASH_REMATCH[1]}" ${AGENT_CONFIG}
        elif echo " $*" | grep -Eq -- '--ip-report-period[= ][0-9]+'; then
            ip_report_val=$(echo " $*" | sed -n 's/.*--ip-report-period[= ][[:space:]]*\([0-9]\{1,\}\).*/\1/p')
            [ -n "$ip_report_val" ] && update_config_value "ipReportPeriod" "$ip_report_val" "$AGENT_CONFIG"
        fi
    fi

    echo -e "探针配置 ${green}修改成功，请稍等探针重启生效${plain}"

    daemon_reload
    service_enable
    service_restart

    # 等待服务启动并检查状态
    echo -e "正在检查探针状态..."

    # 等待最多15秒检查服务状态
    for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15; do
        sleep 1
        service_started=false

        if [ "$os_alpine" = 1 ]; then
            if rc-service server-agent status >/dev/null 2>&1; then
                service_started=true
            elif [ -f /run/server-agent.pid ] && kill -0 "$(cat /run/server-agent.pid 2>/dev/null)" 2>/dev/null; then
                service_started=true
            fi
        elif [ "$os_macos" = 1 ]; then
            # macOS需要更详细的检查
            if launchctl list | grep com.serverstatus.agent >/dev/null 2>&1; then
                # 检查进程是否真正在运行
                agent_status=$(launchctl list | grep com.serverstatus.agent)
                if echo "$agent_status" | grep -v "^-" >/dev/null 2>&1; then
                    service_started=true
                fi
            fi
        else
            if systemctl is-active server-agent >/dev/null 2>&1; then
                service_started=true
            fi
        fi

        if [ "$service_started" = true ]; then
            echo -e "${green}探针服务启动成功！${plain}"

            if [ "$os_alpine" = 1 ]; then
                sleep 1
                if [ -s "/var/log/server-agent.log" ]; then
                    echo -e "${green}探针日志正常生成 (/var/log/server-agent.log)${plain}"
                fi
            elif [ "$os_macos" = 1 ]; then
                sleep 2  # 等待日志写入
                if [ -s "/tmp/server-agent.log" ]; then
                    echo -e "${green}探针日志正常生成 (/tmp/server-agent.log)${plain}"
                elif [ -s "/tmp/server-agent_error.log" ]; then
                    echo -e "${yellow}探针日志已生成 (/tmp/server-agent_error.log)${plain}"
                else
                    echo -e "${yellow}探针已启动，等待日志生成...${plain}"
                fi
            fi
            break
        fi

        # 每5秒显示一次进度
        if [ $((i % 5)) -eq 0 ]; then
            echo -e "等待服务启动... ($i/15)"
        fi

        if [ $i -eq 15 ]; then
            echo -e "${yellow}探针服务启动超时，正在自动收集诊断信息...${plain}"
            if [ "$os_alpine" = 1 ]; then
                echo -e "${yellow}--- 服务状态 (rc-service server-agent status) ---${plain}"
                rc-service server-agent status 2>&1 || true
                if [ -s /var/log/server-agent.log ]; then
                    echo -e "${yellow}--- 最近运行日志 (/var/log/server-agent.log) ---${plain}"
                    tail -n 20 /var/log/server-agent.log 2>/dev/null
                elif [ -s /var/log/server-agent_error.log ]; then
                    echo -e "${yellow}--- 最近运行日志 (/var/log/server-agent_error.log) ---${plain}"
                    tail -n 20 /var/log/server-agent_error.log 2>/dev/null
                fi
            elif [ "$os_macos" = 1 ]; then
                echo -e "${yellow}--- LaunchAgent 状态 ---${plain}"
                launchctl list | grep com.serverstatus.agent || true
                if [ -s /tmp/server-agent.log ]; then
                    echo -e "${yellow}--- 最近运行日志 (/tmp/server-agent.log) ---${plain}"
                    tail -n 20 /tmp/server-agent.log 2>/dev/null
                elif [ -s /tmp/server-agent_error.log ]; then
                    echo -e "${yellow}--- 最近运行日志 (/tmp/server-agent_error.log) ---${plain}"
                    tail -n 20 /tmp/server-agent_error.log 2>/dev/null
                fi
            else
                echo -e "${yellow}--- 服务状态 (systemctl status server-agent) ---${plain}"
                systemctl status server-agent --no-pager -l 2>&1 || true
                echo -e "${yellow}--- 最近日志 (journalctl -u server-agent -n 20) ---${plain}"
                journalctl -u server-agent -n 20 --no-pager 2>&1 || true
            fi
            echo -e "${yellow}提示: 请检查 $AGENT_CONFIG 中的配置参数及网络连通性。${plain}"
        fi
    done

    if [[ $# == 0 ]]; then
        echo -e "探针安装/配置完毕！"
        before_show_menu
    fi
}

show_agent_log() {
    echo -e "> 获取探针日志"

    if [ "$os_alpine" = 1 ]; then
        # Alpine使用OpenRC，查看日志文件
        init_openrc_env
        echo -e "${green}=== 探针状态 ===${plain}"
        service_status

        # 兼容旧版本：若旧错误日志存在内容且运行日志为空，合并至运行日志
        if [ -s "/var/log/server-agent_error.log" ]; then
            if [ ! -s "/var/log/server-agent.log" ]; then
                cat /var/log/server-agent_error.log >> /var/log/server-agent.log 2>/dev/null || true
            fi
        fi

        local log_target="/var/log/server-agent.log"
        if [ ! -s "$log_target" ] && [ -s "/var/log/server-agent_error.log" ]; then
            log_target="/var/log/server-agent_error.log"
        fi

        echo -e "\n${green}=== 运行日志 (${log_target}) ===${plain}"
        echo -e "${yellow}提示: 按 Ctrl+C 可退出日志查看并返回菜单${plain}\n"

        if [ -f "$log_target" ]; then
            trap 'echo ""; trap - INT' INT
            tail -n 30 -f "$log_target"
            trap - INT
        else
            echo -e "${yellow}运行日志文件不存在，请检查服务是否正在运行${plain}"
        fi
    elif [ "$os_macos" = 1 ]; then
        # macOS使用LaunchAgent，查看日志文件
        echo -e "正在检查探针状态..."

        # 详细的诊断信息
        echo -e "${green}=== 诊断信息 ===${plain}"

        # 检查文件是否存在
        if [ -f "$AGENT_PATH/server-agent" ]; then
            echo -e "✓ 探针程序存在: $AGENT_PATH/server-agent"
            ls -la "$AGENT_PATH/server-agent"
        else
            echo -e "✗ 探针程序不存在: $AGENT_PATH/server-agent"
        fi

        # 检查配置文件
        if [ -f "$AGENT_CONFIG" ]; then
            echo -e "✓ 配置文件存在: $AGENT_CONFIG"
        else
            echo -e "✗ 配置文件不存在: $AGENT_CONFIG"
        fi

        # 检查LaunchAgent文件
        if [ -f "$AGENT_LAUNCHD_SERVICE" ]; then
            echo -e "✓ LaunchAgent配置存在: $AGENT_LAUNCHD_SERVICE"
            if grep -q '<string>/tmp/server-agent_error.log</string>' "$AGENT_LAUNCHD_SERVICE" 2>/dev/null; then
                sed -i '' 's#/tmp/server-agent_error.log#/tmp/server-agent.log#g' "$AGENT_LAUNCHD_SERVICE" 2>/dev/null || true
            fi
        else
            echo -e "✗ LaunchAgent配置不存在: $AGENT_LAUNCHD_SERVICE"
        fi

        # 检查服务状态
        echo -e "\n${green}=== 服务状态 ===${plain}"
        if launchctl list | grep com.serverstatus.agent >/dev/null 2>&1; then
            echo -e "✓ LaunchAgent已加载"
            launchctl list | grep com.serverstatus.agent
        else
            echo -e "✗ LaunchAgent未加载，尝试启动..."
            service_start
            sleep 3
            if launchctl list | grep com.serverstatus.agent >/dev/null 2>&1; then
                echo -e "✓ LaunchAgent启动成功"
                launchctl list | grep com.serverstatus.agent
            else
                echo -e "✗ LaunchAgent启动失败"
            fi
        fi

        # 尝试手动测试
        echo -e "\n${green}=== 手动测试 ===${plain}"
        if [ -f "$AGENT_PATH/server-agent" ] && [ -f "$AGENT_CONFIG" ]; then
            echo -e "尝试手动启动探针（测试5秒）..."
            cd "$AGENT_PATH"
            timeout 5 ./server-agent 2>&1 | head -10 || echo "手动启动测试完成"
        fi

        # 兼容旧版本：合并旧错误日志
        if [ -s "/tmp/server-agent_error.log" ]; then
            if [ ! -s "/tmp/server-agent.log" ]; then
                cat /tmp/server-agent_error.log >> /tmp/server-agent.log 2>/dev/null || true
            fi
        fi

        local macos_log="/tmp/server-agent.log"
        if [ ! -s "$macos_log" ] && [ -s "/tmp/server-agent_error.log" ]; then
            macos_log="/tmp/server-agent_error.log"
        fi

        # 显示日志
        echo -e "\n${green}=== 运行日志 (${macos_log}) ===${plain}"
        if [ -f "$macos_log" ]; then
            tail -n 30 "$macos_log"
        else
            echo -e "日志文件不存在"
        fi

        echo -e "\n${yellow}如果问题持续，请尝试：${plain}"
        echo -e "1. 手动启动: cd $AGENT_PATH && ./server-agent"
        echo -e "2. 检查配置: cat $AGENT_CONFIG"
        echo -e "3. 重新安装: ./server-status.sh uninstall_agent && ./server-status.sh install_agent"

    else
        # 其他系统使用systemd
        journalctl -xf -u server-agent.service
    fi

    if [[ $# == 0 ]]; then
        before_show_menu
    fi
}

uninstall_agent() {
    echo -e "> 卸载 探针"

    service_disable
    service_stop

    if [ "$os_alpine" = 1 ]; then
        rm -rf $AGENT_OPENRC_SERVICE
        rm -f /run/server-agent.pid /var/run/server-agent.pid
        rm -f /var/log/server-agent.log /var/log/server-agent_error.log
    elif [ "$os_macos" = 1 ]; then
        rm -rf $AGENT_LAUNCHD_SERVICE
        # 清理日志文件（使用当前用户权限）
        rm -f /tmp/server-agent.log /tmp/server-agent_error.log 2>/dev/null || true
    else
        rm -rf $AGENT_SERVICE
        daemon_reload
    fi

    # 删除探针文件
    if [ "$os_macos" = 1 ]; then
        echo "正在删除探针文件..."

        # 首先尝试删除探针二进制文件
        if [ -f "$AGENT_PATH/server-agent" ]; then
            if [ -w "$AGENT_PATH/server-agent" ]; then
                rm -f "$AGENT_PATH/server-agent"
                echo "探针程序已删除"
            else
                sudo rm -f "$AGENT_PATH/server-agent" 2>/dev/null && echo "探针程序已删除" || echo "删除探针程序失败"
            fi
        fi

        # 删除配置文件
        if [ -f "$AGENT_CONFIG" ]; then
            if [ -w "$AGENT_CONFIG" ]; then
                rm -f "$AGENT_CONFIG"
                echo "配置文件已删除"
            else
                sudo rm -f "$AGENT_CONFIG" 2>/dev/null && echo "配置文件已删除" || echo "删除配置文件失败"
            fi
        fi

        # 尝试删除目录（如果为空）
        if [ -d "$AGENT_PATH" ]; then
            # 检查目录是否为空
            if [ -z "$(ls -A $AGENT_PATH 2>/dev/null)" ]; then
                if [ -w "$(dirname $AGENT_PATH)" ]; then
                    rmdir "$AGENT_PATH" 2>/dev/null && echo "探针目录已删除"
                else
                    sudo rmdir "$AGENT_PATH" 2>/dev/null && echo "探针目录已删除"
                fi
            else
                echo "探针目录不为空，保留目录: $AGENT_PATH"
                echo "剩余文件:"
                ls -la "$AGENT_PATH" 2>/dev/null || sudo ls -la "$AGENT_PATH" 2>/dev/null
            fi
        fi

        # 尝试删除父目录（如果为空）
        if [ -d "/opt/server-status" ]; then
            if [ -z "$(ls -A /opt/server-status 2>/dev/null)" ]; then
                if [ -w "/opt" ]; then
                    rmdir "/opt/server-status" 2>/dev/null && echo "server-status目录已删除"
                else
                    sudo rmdir "/opt/server-status" 2>/dev/null && echo "server-status目录已删除"
                fi
            fi
        fi

        echo "探针卸载完成"
    else
        rm -rf $AGENT_PATH
    fi

    clean_all

    if [[ $# == 0 ]]; then
        before_show_menu
    fi
}

restart_agent() {
    echo -e "> 重启 探针"

    service_restart

    if [[ $# == 0 ]]; then
        before_show_menu
    fi
}

clean_all() {
    if [ -z "$(ls -A ${BASE_PATH})" ]; then
        rm -rf ${BASE_PATH}
    fi
}

update_dashboard() {
    echo -e "> 更新探针面板"

    install_base || return 1

    echo -e "正在获取探针面板版本号"

    local version=$(curl -m 10 -sL "https://api.github.com/repos/xos/serverstatus/releases/latest" | grep "tag_name" | head -n 1 | awk -F ":" '{print $2}' | sed 's/\"//g;s/,//g;s/ //g')
    if [ ! -n "$version" ]; then
        version=$(curl -m 10 -sL "${R2_URL:-https://assets.cnic.eu.org}/serverdash/index.json" | grep -o '"tag_name":"[^"]*"' | head -n 1 | awk -F '"' '{print $4}')
    fi

    if [ ! -n "$version" ]; then
        echo -e "获取版本号失败！"
        return 0
    else
        echo -e "当前最新版本为: ${version}"
    fi

    # 探针面板文件夹
    if [ ! -z "${DASHBOARD_PATH}" ]; then
        mkdir -p $DASHBOARD_PATH
        chmod 777 -R $DASHBOARD_PATH
    fi
    echo "正在获取探针面板"
    if [ -z "$CN" ]; then
        DASHBOARD_URL="https://${GITHUB_URL}/xos/serverstatus/releases/download/${version}/server-dash-linux-${os_arch}.zip"
        DASHBOARD_FALLBACK_URL=""
    else
        DASHBOARD_URL="https://${GITHUB_URL}/serverdash/${version}/server-dash-linux-${os_arch}.zip"
        DASHBOARD_FALLBACK_URL="https://github.com/xos/serverstatus/releases/download/${version}/server-dash-linux-${os_arch}.zip"
    fi
    echo -e "正在下载探针面板"
    if ! download_release_archive "$DASHBOARD_URL" "$DASHBOARD_FALLBACK_URL" "server-dash-linux-${os_arch}.zip"; then
        err "Release 下载失败，请检查中国镜像或 GitHub 的网络连接"
        return 1
    fi
    unzip -qo server-dash-linux-${os_arch}.zip &&
        mv server-dash-linux-${os_arch} server-dash &&
        mv server-dash $DASHBOARD_PATH &&
        rm -rf server-dash-linux-${os_arch}.zip
        systemctl restart server-dash.service

    if [[ $# == 0 ]]; then
        echo -e "更新完毕！"
        before_show_menu
    fi
}

restart_dashboard() {
    echo -e "> 重启探针面板"

    systemctl restart server-dash.service

    if [[ $# == 0 ]]; then
        before_show_menu
    fi
}

show_dashboard_log() {
    echo -e "> 获取探针面板日志"

    journalctl -xf -u server-dash.service

    if [[ $# == 0 ]]; then
        before_show_menu
    fi
}

show_usage() {
    echo "探针 管理脚本使用方法: "
    echo "--------------------------------------------------------"
    echo "./server-status.sh                            - 显示管理菜单"
    echo "./server-status.sh install_agent              - 安装探针"
    echo "./server-status.sh install_agent <host> <port> <secret> [options]"
    echo "                                              - 安装探针并配置参数"
    echo "  示例: ./server-status.sh install_agent grpc.example.com 443 your-secret --tls"
    echo "  支持的选项:"
    echo "    --tls                     启用TLS加密"
    echo "    --insecure-tls           跳过TLS证书验证"
    echo "    --debug                  启用调试模式"
    echo "    --gpu                    启用GPU监控"
    echo "    --temperature            启用温度监控"
    echo "    --disable-auto-update    禁用自动更新"
    echo "    --disable-command-execute 禁用命令执行"
    echo "    --disable-nat            禁用内网穿透"
    echo "    --report-delay <seconds> 设置上报间隔(1-4秒)"
    echo "./server-status.sh update_agent               - 更新探针"
    echo "./server-status.sh modify_agent_config        - 修改探针配置"
    echo "./server-status.sh show_agent_log             - 探针状态"
    echo "./server-status.sh uninstall_agent            - 卸载探针"
    echo "./server-status.sh restart_agent              - 重启探针"
    echo "./server-status.sh update_script              - 更新脚本"
    echo "./server-status.sh update_dashboard           - 更新探针面板"
    echo "./server-status.sh restart_dashboard          - 重启探针面板"
    echo "./server-status.sh show_dashboard_log         - 查看探针面板日志"
    echo "--------------------------------------------------------"
    echo "配置文件位置: ${AGENT_CONFIG}"
    echo "服务文件位置: ${AGENT_SERVICE}"
    echo "--------------------------------------------------------"
}

show_menu() {
    clear
    echo -e "
    =========================
    ${green}探针管理脚本${plain} ${red}[${VERSION}]${plain}
    =========================
    ${green}1.${plain} 安装 探针
    ${green}2.${plain} 更新 探针
    ${green}3.${plain} 探针 状态
    ${green}4.${plain} 卸载 探针
    ${green}5.${plain} 重启 探针
    ${green}6.${plain} 修改探针配置
    —————————————————————————
    ${green}7.${plain} 更新探针面板
    ${green}8.${plain} 重启探针面板
    ${green}9.${plain} 查看探针面板日志
    —————————————————————————
    ${green}0.${plain} 更新脚本
    ${green}00.${plain} 退出脚本
    =========================
    "
    echo && read -r -p "请输入选择 [0-9]: " num

    case "${num}" in
    00)
        exit 0
        ;;
    1)
        install_agent
        ;;
    2)
        update_agent
        ;;
    3)
        show_agent_log
        ;;
    4)
        uninstall_agent
        ;;
    5)
        restart_agent
        ;;
    6)
        set_agent
        ;;
    7)
        update_dashboard
        ;;
    8)
        restart_dashboard
        ;;
    9)
        show_dashboard_log
        ;;
    0)
        update_script
        ;;
    *)
        echo -e "${red}请输入正确的数字 [0-9]${plain}"
        before_show_menu
        ;;
    esac
}

pre_check

if [[ $# > 0 ]]; then
    case $1 in
    "install_dashboard")
        install_dashboard 0
        ;;
    "modify_dashboard_config")
        modify_dashboard_config 0
        ;;
    "start_dashboard")
        start_dashboard 0
        ;;
    "stop_dashboard")
        stop_dashboard 0
        ;;
    "restart_and_update")
        restart_and_update 0
        ;;
    "show_dashboard_log")
        show_dashboard_log 0
        ;;
    "uninstall_dashboard")
        uninstall_dashboard 0
        ;;
    "install_agent")
        shift
        if [ $# -ge 3 ]; then
            install_agent "$@"
        else
            install_agent 0
        fi
        ;;
    "update_agent")
        update_agent 0
        ;;
    "modify_agent_config")
        modify_agent_config 0
        ;;
    "show_agent_log")
        show_agent_log 0
        ;;
    "uninstall_agent")
        uninstall_agent 0
        ;;
    "restart_agent")
        restart_agent 0
        ;;
    "update_script")
        update_script 0
        ;;
    "update_dashboard")
        update_dashboard 0
        ;;
    "restart_dashboard")
        restart_dashboard 0
        ;;
    "show_dashboard_log")
        show_dashboard_log 0
        ;;
    *) show_usage ;;
    esac
else
    show_menu
fi
