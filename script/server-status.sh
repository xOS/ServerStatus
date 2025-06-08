#!/bin/bash
#========================================================
#   System Required: CentOS 7+ / Debian 8+ / Ubuntu 16+ /
#   Arch 未测试
#   Description: 探针安装脚本
#   Github: https://github.com/xOS/ServerStatus
#========================================================

BASE_PATH="/opt/server-status"
DASHBOARD_PATH="${BASE_PATH}/dashboard"
AGENT_PATH="${BASE_PATH}/agent"
AGENT_SERVICE="/etc/systemd/system/server-agent.service"
AGENT_CONFIG="${AGENT_PATH}/config.yml"
AGENT_OPENRC_SERVICE="/etc/init.d/server-agent"
AGENT_LAUNCHD_SERVICE="$HOME/Library/LaunchAgents/com.serverstatus.agent.plist"
VERSION="v0.2.7"

red='\033[0;31m'
green='\033[0;32m'
yellow='\033[0;33m'
plain='\033[0m'
export PATH=$PATH:/usr/local/bin

os_arch=""
os_alpine=0
os_macos=0

sudo() {
    myEUID=$(id -ru)
    if [ "$myEUID" -ne 0 ]; then
        if command -v sudo > /dev/null 2>&1; then
            command sudo "$@"
        else
            err "错误: 您的系统未安装 sudo，因此无法进行该项操作。"
            exit 1
        fi
    else
        "$@"
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
        rc-update add server-agent default
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
        rc-service server-agent stop
    elif [ "$os_macos" = 1 ]; then
        launchctl stop com.serverstatus.agent
    else
        systemctl stop server-agent
    fi
}

service_restart() {
    if [ "$os_alpine" = 1 ]; then
        rc-service server-agent restart
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
        rc-service server-agent status
    elif [ "$os_macos" = 1 ]; then
        launchctl list | grep com.serverstatus.agent || echo "服务未运行"
    else
        systemctl status server-agent
    fi
}

service_disable() {
    if [ "$os_alpine" = 1 ]; then
        rc-update del server-agent default
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

geo_check() {
    api_list="https://blog.cloudflare.com/cdn-cgi/trace https://dash.cloudflare.com/cdn-cgi/trace https://cf-ns.com/cdn-cgi/trace"
    ua="Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/134.0.0.0 Safari/537.36"
    set -- $api_list
    for url in $api_list; do
        text="$(curl -A "$ua" -m 10 -s $url)"
        endpoint="$(echo $text | sed -n 's/.*h=\([^ ]*\).*/\1/p')"
        if echo $text | grep -qw 'CN'; then
            isCN=true
            break
        elif echo $url | grep -q $endpoint; then
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
            if [ -f /etc/alpine-release ]; then
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
        GITHUB_RELEASE_URL="github.com/xos/serveragent/releases/latest/download"
    else
        GITHUB_RAW_URL="gitee.com/ten/ServerStatus/raw/master"
        GITHUB_URL="gitee.com"
        GITHUB_RELEASE_URL="gitee.com/ten/ServerAgent/releases/latest/download"
    fi
}

confirm() {
    if [[ $# > 1 ]]; then
        echo && read -e -p "$1 [默认$2]: " temp
        if [[ x"${temp}" == x"" ]]; then
            temp=$2
        fi
    else
        read -e -p "$1 [y/n]: " temp
    fi
    if [[ x"${temp}" == x"y" || x"${temp}" == x"Y" ]]; then
        return 0
    else
        return 1
    fi
}

update_script() {
    echo -e "> 更新脚本"

    curl -sL https://${GITHUB_RAW_URL}/script/server-status.sh -o /tmp/server-status.sh
    new_version=$(cat /tmp/server-status.sh | grep "VERSION" | head -n 1 | awk -F "=" '{print $2}' | sed 's/\"//g;s/,//g;s/ //g')
    if [ ! -n "$new_version" ]; then
        echo -e "脚本获取失败，请检查本机能否链接 https://${GITHUB_RAW_URL}/script/server-status.sh"
        return 1
    fi
    echo -e "当前最新版本为: ${new_version}"
    mv -f /tmp/server-status.sh ./server-status.sh && chmod a+x ./server-status.sh

    echo -e "3s后执行新脚本"
    sleep 3s
    clear
    exec ./server-status.sh
    exit 0
}

before_show_menu() {
    echo && echo -n -e "${yellow}* 按回车返回主菜单 *${plain}" && read temp
    show_menu
}

install_base() {
    if [ "$os_alpine" = 1 ] || [ "$os_macos" = 1 ]; then
        # Alpine和macOS系统不需要getenforce，只检查基本工具
        (command -v curl >/dev/null 2>&1 && command -v wget >/dev/null 2>&1 && command -v unzip >/dev/null 2>&1) ||
            (install_soft curl wget unzip)
    else
        # 其他系统检查包括getenforce
        (command -v curl >/dev/null 2>&1 && command -v wget >/dev/null 2>&1 && command -v unzip >/dev/null 2>&1 && command -v getenforce >/dev/null 2>&1) ||
            (install_soft curl wget unzip)
    fi
}

install_soft() {
	# 根据不同系统使用相应的包管理器
    if [ "$os_alpine" = 1 ]; then
        # Alpine Linux 使用 apk
        apk update && apk add $*
    elif [ "$os_macos" = 1 ]; then
        # macOS 使用 Homebrew
        if command -v brew >/dev/null 2>&1; then
            brew install $*
        else
            echo -e "${yellow}未检测到Homebrew，正在安装...${plain}"
            /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
            brew install $*
        fi
    elif command -v yum >/dev/null 2>&1; then
        # RHEL/CentOS/Fedora 使用 yum
        yum makecache && yum install $* selinux-policy -y
    elif command -v apt >/dev/null 2>&1; then
        # Debian/Ubuntu 使用 apt
        apt update && apt install $* selinux-utils -y
    elif command -v pacman >/dev/null 2>&1; then
        # Arch Linux 使用 pacman
        pacman -Syu $* base-devel --noconfirm && install_arch
    elif command -v apt-get >/dev/null 2>&1; then
        # 旧版 Debian/Ubuntu 使用 apt-get
        apt-get update && apt-get install $* selinux-utils -y
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

install_agent() {
    install_base
    selinux

    echo -e "> 安装探针"

    echo -e "正在获取探针版本号"

    local version=$(curl -m 10 -sL "https://api.github.com/repos/xos/serveragent/releases/latest" | grep "tag_name" | head -n 1 | awk -F ":" '{print $2}' | sed 's/\"//g;s/,//g;s/ //g')
    if [ ! -n "$version" ]; then
        version=$(curl -m 10 -sL "https://gitee.com/api/v5/repos/Ten/ServerAgent/releases/latest" | awk -F '"' '{for(i=1;i<=NF;i++){if($i=="tag_name"){print $(i+2)}}}')
    fi

    if [ ! -n "$version" ]; then
        echo -e "获取版本号失败！"
        return 0
    else
        echo -e "当前最新版本为: ${version}"
    fi

    # 探针文件夹
    if [ ! -z "${AGENT_PATH}" ]; then
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
        else
            AGENT_URL="https://${GITHUB_URL}/Ten/ServerAgent/releases/download/${version}/server-agent_darwin_${os_arch}.zip"
        fi
        AGENT_ZIP="server-agent_darwin_${os_arch}.zip"
    else
        if [ -z "$CN" ]; then
            AGENT_URL="https://${GITHUB_URL}/xos/serveragent/releases/download/${version}/server-agent_linux_${os_arch}.zip"
        else
            AGENT_URL="https://${GITHUB_URL}/Ten/ServerAgent/releases/download/${version}/server-agent_linux_${os_arch}.zip"
        fi
        AGENT_ZIP="server-agent_linux_${os_arch}.zip"
    fi

    echo -e "正在下载探针"
    wget -t 2 -T 60 -O $AGENT_ZIP $AGENT_URL >/dev/null 2>&1
    if [ $? != 0 ]; then
        err "Release 下载失败，请检查本机能否连接 ${GITHUB_URL}"
        return 1
    fi
    unzip -qo $AGENT_ZIP &&
        chmod +x server-agent &&
        mv server-agent $AGENT_PATH &&
        rm -rf $AGENT_ZIP README.md

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
    echo "正在下载配置文件模板"
    wget -t 2 -T 10 -O $AGENT_CONFIG https://${GITHUB_RAW_URL}/script/config.yml >/dev/null 2>&1
    if [[ $? != 0 ]]; then
        echo -e "${red}配置文件下载失败，请检查本机能否连接 ${GITHUB_RAW_URL}${plain}"
        return 1
    fi

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

    # 根据系统类型下载相应的服务文件
    if [ "$os_alpine" = 1 ]; then
        echo "正在下载OpenRC服务文件"
        wget -t 2 -T 10 -O $AGENT_OPENRC_SERVICE https://${GITHUB_RAW_URL}/script/server-agent.openrc >/dev/null 2>&1
        if [[ $? != 0 ]]; then
            echo -e "${red}OpenRC服务文件下载失败，请检查本机能否连接 ${GITHUB_RAW_URL}${plain}"
            return 1
        fi
        chmod +x $AGENT_OPENRC_SERVICE
    elif [ "$os_macos" = 1 ]; then
        echo "正在下载LaunchAgent配置文件"
        # 确保LaunchAgents目录存在
        mkdir -p "$HOME/Library/LaunchAgents"
        wget -t 2 -T 10 -O $AGENT_LAUNCHD_SERVICE https://${GITHUB_RAW_URL}/script/com.serverstatus.agent.plist >/dev/null 2>&1
        if [[ $? != 0 ]]; then
            echo -e "${red}LaunchAgent配置文件下载失败，请检查本机能否连接 ${GITHUB_RAW_URL}${plain}"
            return 1
        fi
    else
        # 其他系统使用systemd
        echo "正在下载systemd服务文件"
        wget -t 2 -T 10 -O $AGENT_SERVICE https://${GITHUB_RAW_URL}/script/server-agent.service >/dev/null 2>&1
        if [[ $? != 0 ]]; then
            echo -e "${red}Service文件下载失败，请检查本机能否连接 ${GITHUB_RAW_URL}${plain}"
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

    echo -e "正在获取探针版本号"

    local version=$(curl -m 10 -sL "https://api.github.com/repos/xos/serveragent/releases/latest" | grep "tag_name" | head -n 1 | awk -F ":" '{print $2}' | sed 's/\"//g;s/,//g;s/ //g')
	if [ ! -n "$version" ]; then
        version=$(curl -m 10 -sL "https://gitee.com/api/v5/repos/Ten/ServerAgent/releases/latest" | awk -F '"' '{for(i=1;i<=NF;i++){if($i=="tag_name"){print $(i+2)}}}')
    fi

    if [ ! -n "$version" ]; then
        echo -e "获取版本号失败！"
        return 0
    else
        echo -e "当前最新版本为: ${version}"
    fi

    # 探针文件夹
    if [ ! -z "${AGENT_PATH}" ]; then
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
        else
            AGENT_URL="https://${GITHUB_URL}/Ten/ServerAgent/releases/download/${version}/server-agent_darwin_${os_arch}.zip"
        fi
        AGENT_ZIP="server-agent_darwin_${os_arch}.zip"
    else
        if [ -z "$CN" ]; then
            AGENT_URL="https://${GITHUB_URL}/xos/serveragent/releases/download/${version}/server-agent_linux_${os_arch}.zip"
        else
            AGENT_URL="https://${GITHUB_URL}/Ten/ServerAgent/releases/download/${version}/server-agent_linux_${os_arch}.zip"
        fi
        AGENT_ZIP="server-agent_linux_${os_arch}.zip"
    fi

    echo -e "正在下载探针"
    wget -t 2 -T 60 -O $AGENT_ZIP $AGENT_URL >/dev/null 2>&1
    if [ $? != 0 ]; then
        err "Release 下载失败，请检查本机能否连接 ${GITHUB_URL}"
        return 1
    fi
    unzip -qo $AGENT_ZIP &&
        chmod +x server-agent &&
        mv server-agent $AGENT_PATH &&
        rm -rf $AGENT_ZIP README.md

    # 检查配置文件是否存在，如果不存在则下载
    if [[ ! -f ${AGENT_CONFIG} ]]; then
        echo "配置文件不存在，正在下载配置文件模板"
        wget -t 2 -T 10 -O $AGENT_CONFIG https://${GITHUB_RAW_URL}/script/config.yml >/dev/null 2>&1
        if [[ $? != 0 ]]; then
            echo -e "${yellow}配置文件下载失败，将使用默认配置${plain}"
        fi
    fi

    service_restart

    if [[ $# == 0 ]]; then
        echo -e "更新完毕！"
        before_show_menu
    fi
}

set_host(){
    read -ep "请输入一个解析到探针面板所在IP的域名: " grpc_host
        [[ -z "${grpc_host}" ]] && echo "已取消输入..." && exit 1
}
set_port(){
    read -ep "请输入探针面板 GRPC 端口（默认：2222）: " grpc_port
        [[ -z "${grpc_port}" ]] && grpc_port=2222
}
set_secret(){
    read -ep "请输入探针密钥: " client_secret
        [[ -z "${client_secret}" ]] && echo "已取消输入..." && exit 1
}
# 更新配置文件中的值
update_config_value() {
    local key=$1
    local value=$2
    local config_file=$3

    # 处理不同类型的值
    if [[ "$value" == "true" || "$value" == "false" ]]; then
        # 布尔值不需要引号
        if [[ "$OSTYPE" == "darwin"* ]]; then
            sed -i '' "s/^${key}:.*/${key}: ${value}/" "$config_file"
        else
            sed -i "s/^${key}:.*/${key}: ${value}/" "$config_file"
        fi
    elif [[ "$value" =~ ^[0-9]+$ ]]; then
        # 数字不需要引号
        if [[ "$OSTYPE" == "darwin"* ]]; then
            sed -i '' "s/^${key}:.*/${key}: ${value}/" "$config_file"
        else
            sed -i "s/^${key}:.*/${key}: ${value}/" "$config_file"
        fi
    else
        # 字符串需要引号
        if [[ "$OSTYPE" == "darwin"* ]]; then
            sed -i '' "s/^${key}:.*/${key}: \"${value}\"/" "$config_file"
        else
            sed -i "s/^${key}:.*/${key}: \"${value}\"/" "$config_file"
        fi
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
	    read -e -p "(默认: 取消):" modify
        [[ -z "${modify}" ]] && echo "已取消..." && exit 1

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
    read -e -p "请选择配置项 [0-8]: " advanced_option

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
    read -e -p "是否切换状态? [y/n]: " toggle

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

    read -e -p "请输入新的上报间隔 (1-4秒，推荐1秒): " new_delay

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
    read -e -p "是否重启探针以使配置生效? [y/n]: " restart_choice

    if [[ x"${restart_choice}" == x"y" || x"${restart_choice}" == x"Y" ]]; then
        echo "重启探针中..."
        service_restart
        echo -e "探针 已重启完毕！"
    fi

    before_show_menu
}

modify_agent_config() {
    echo -e "> 初始化探针配置"

    # 根据系统类型下载相应的服务文件
    if [ "$os_alpine" = 1 ]; then
        # Alpine使用OpenRC
        wget -t 2 -T 10 -O $AGENT_OPENRC_SERVICE https://${GITHUB_RAW_URL}/script/server-agent.openrc >/dev/null 2>&1
        if [[ $? != 0 ]]; then
            echo -e "${red}OpenRC服务文件下载失败，请检查本机能否连接 ${GITHUB_RAW_URL}${plain}"
            return 0
        fi
        chmod +x $AGENT_OPENRC_SERVICE
    elif [ "$os_macos" = 1 ]; then
        # macOS使用LaunchAgent
        mkdir -p "$HOME/Library/LaunchAgents"
        wget -t 2 -T 10 -O $AGENT_LAUNCHD_SERVICE https://${GITHUB_RAW_URL}/script/com.serverstatus.agent.plist >/dev/null 2>&1
        if [[ $? != 0 ]]; then
            echo -e "${red}LaunchAgent配置文件下载失败，请检查本机能否连接 ${GITHUB_RAW_URL}${plain}"
            return 0
        fi
    else
        # 其他系统使用systemd
        wget -t 2 -T 10 -O $AGENT_SERVICE https://${GITHUB_RAW_URL}/script/server-agent.service >/dev/null 2>&1
        if [[ $? != 0 ]]; then
            echo -e "${red}Service文件下载失败，请检查本机能否连接 ${GITHUB_RAW_URL}${plain}"
            return 0
        fi
    fi

    # 确保配置文件存在
    if [[ ! -f ${AGENT_CONFIG} ]]; then
        echo "配置文件不存在，正在下载配置文件模板"
        wget -t 2 -T 10 -O $AGENT_CONFIG https://${GITHUB_RAW_URL}/script/config.yml >/dev/null 2>&1
        if [[ $? != 0 ]]; then
            echo -e "${red}配置文件下载失败，请检查本机能否连接 ${GITHUB_RAW_URL}${plain}"
            return 0
        fi
    fi

    if [[ $# -lt 3 ]]; then
        echo "请先在管理面板上添加探针服务，记录下密钥" &&
            read -ep "请输入一个解析到探针面板所在IP的域名: " grpc_host &&
            read -ep "请输入探针面板 GRPC 端口（默认：2222）: " grpc_port &&
            read -ep "请输入探针密钥: " client_secret
        if [[ -z "${grpc_host}" || -z "${client_secret}" ]]; then
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
        # 处理use-gitee-to-upgrade参数
        if [[ "$*" == *"--use-gitee-to-upgrade"* ]]; then
            update_config_value "useGiteeToUpgrade" "true" ${AGENT_CONFIG}
        fi
        # 处理report-delay参数
        if [[ "$*" =~ --report-delay[[:space:]]+([0-9]+) ]]; then
            update_config_value "reportDelay" "${BASH_REMATCH[1]}" ${AGENT_CONFIG}
        fi
        # 处理ip-report-period参数
        if [[ "$*" =~ --ip-report-period[[:space:]]+([0-9]+) ]]; then
            update_config_value "ipReportPeriod" "${BASH_REMATCH[1]}" ${AGENT_CONFIG}
        fi
    fi

    echo -e "探针配置 ${green}修改成功，请稍等探针重启生效${plain}"

    daemon_reload
    service_enable
    service_restart

    # 等待服务启动并检查状态
    echo -e "正在检查探针状态..."

    # 等待最多15秒检查服务状态
    for i in {1..15}; do
        sleep 1
        service_started=false

        if [ "$os_alpine" = 1 ]; then
            if rc-service server-agent status >/dev/null 2>&1; then
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

            # macOS下额外检查日志文件是否有内容
            if [ "$os_macos" = 1 ]; then
                sleep 2  # 等待日志写入
                if [ -f "/tmp/server-agent.log" ] && [ -s "/tmp/server-agent.log" ]; then
                    echo -e "${green}探针日志正常生成${plain}"
                elif [ -f "/tmp/server-agent_error.log" ] && [ -s "/tmp/server-agent_error.log" ]; then
                    echo -e "${yellow}探针启动但有错误，请查看日志${plain}"
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
            echo -e "${yellow}探针服务启动超时，请检查配置或查看日志${plain}"
            echo -e "您可以使用以下命令诊断问题："
            if [ "$os_alpine" = 1 ]; then
                echo -e "  rc-service server-agent status"
                echo -e "  tail -f /var/log/server-agent.log"
            elif [ "$os_macos" = 1 ]; then
                echo -e "  launchctl list | grep com.serverstatus.agent"
                echo -e "  ./server-status.sh show_agent_log  # 查看详细诊断"
                echo -e "  cd $AGENT_PATH && ./server-agent  # 手动测试"
            else
                echo -e "  systemctl status server-agent"
                echo -e "  journalctl -u server-agent"
            fi
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
        if [ -f "/var/log/server-agent.log" ]; then
            tail -f /var/log/server-agent.log
        else
            echo -e "${yellow}日志文件不存在，请检查服务是否正在运行${plain}"
            service_status
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

        # 显示日志
        echo -e "\n${green}=== 日志文件 ===${plain}"
        if [ -f "/tmp/server-agent.log" ]; then
            echo -e "标准输出日志 (最近20行):"
            tail -20 /tmp/server-agent.log
        else
            echo -e "标准输出日志文件不存在"
        fi

        if [ -f "/tmp/server-agent_error.log" ]; then
            echo -e "\n错误日志 (最近20行):"
            tail -20 /tmp/server-agent_error.log
        else
            echo -e "错误日志文件不存在"
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

    echo -e "正在获取探针面板版本号"

    local version=$(curl -m 10 -sL "https://api.github.com/repos/xos/serverstatus/releases/latest" | grep "tag_name" | head -n 1 | awk -F ":" '{print $2}' | sed 's/\"//g;s/,//g;s/ //g')
    if [ ! -n "$version" ]; then
        version=$(curl -m 10 -sL "https://gitee.com/api/v5/repos/ten/ServerStatus/releases/latest" | awk -F '"' '{for(i=1;i<=NF;i++){if($i=="tag_name"){print $(i+2)}}}')
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
    else
        DASHBOARD_URL="https://${GITHUB_URL}/ten/ServerStatus/releases/download/${version}/server-dash-linux-${os_arch}.zip"
    fi
    echo -e "正在下载探针面板"
    wget -t 2 -T 60 -O server-dash-linux-${os_arch}.zip $DASHBOARD_URL >/dev/null 2>&1
    if [ $? != 0 ]; then
        err "Release 下载失败，请检查本机能否连接 ${GITHUB_URL}"
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
    echo && read -ep "请输入选择 [0-9]: " num

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
