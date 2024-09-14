#!/bin/bash
#========================================================
#   System Required: CentOS 7+ / Debian 8+ / Ubuntu 16+ /
#   Arch 未测试
#   Description: 探针轻量版安装脚本
#   Github: https://github.com/xOS/ServerStatus
#========================================================

BASE_PATH="/opt/server-status"
DASHBOARD_PATH="${BASE_PATH}/dashboard"
AGENT_PATH="${BASE_PATH}/agent"
AGENT_SERVICE="/etc/systemd/system/server-agent.service"
VERSION="v0.1.16"

red='\033[0;31m'
green='\033[0;32m'
yellow='\033[0;33m'
plain='\033[0m'
export PATH=$PATH:/usr/local/bin

os_arch=""
[ -e /etc/os-release ] && grep -i "PRETTY_NAME" /etc/os-release | grep -qi "alpine" && os_alpine='1'

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

err() {
    printf "${red}$*${plain}\n" >&2
}

geo_check() {
    api_list="https://blog.cloudflare.com/cdn-cgi/trace https://dash.cloudflare.com/cdn-cgi/trace https://cf-ns.com/cdn-cgi/trace"
    ua="Mozilla/5.0 (X11; Linux x86_64; rv:60.0) Gecko/20100101 Firefox/81.0"
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
    if uname -m | grep -q 'x86_64'; then
        os_arch="amd64"
    elif uname -m | grep -q 'i386\|i686'; then
        os_arch="386"
    elif uname -m | grep -q 'aarch64\|armv8b\|armv8l'; then
        os_arch="arm64"
    elif uname -m | grep -q 'arm'; then
        os_arch="arm"
    elif uname -m | grep -q 's390x'; then
        os_arch="s390x"
    elif uname -m | grep -q 'riscv64'; then
        os_arch="riscv64"
    fi

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
        Get_Docker_URL="get.docker.com"
        Get_Docker_Argu=" "
        Docker_IMG="ghcr.io\/xos\/server-dash"
        GITHUB_RELEASE_URL="github.com/xos/serveragent/releases/latest/download"
    else
        GITHUB_RAW_URL="fastly.jsdelivr.net/gh/xos/serverstatus@master"
            GITHUB_URL="gitee.com"
            Get_Docker_URL="get.docker.com"
        Get_Docker_Argu=" -s docker --mirror Aliyun"
        Docker_IMG="registry.cn-shanghai.aliyuncs.com\/dns\/server-dash"
        
        curl -s https://purge.jsdelivr.net/gh/xos/serverstatus@master/script/server-status.sh > /dev/null 2>&1
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
    (command -v git >/dev/null 2>&1 && command -v curl >/dev/null 2>&1 && command -v wget >/dev/null 2>&1 && command -v unzip >/dev/null 2>&1 && command -v getenforce >/dev/null 2>&1) ||
        (install_soft curl wget git unzip)
}

install_soft() {
	# Arch官方库不包含selinux等组件
    (command -v yum >/dev/null 2>&1 && yum makecache && yum install $* selinux-policy -y) ||
        (command -v apt >/dev/null 2>&1 && apt update && apt install $* selinux-utils -y) ||
        (command -v pacman >/dev/null 2>&1 && pacman -Syu $* base-devel --noconfirm && install_arch) ||
        (command -v apt-get >/dev/null 2>&1 && apt-get update && apt-get install $* selinux-utils -y)
}

install_dashboard() {
    check_systemd
    install_base

    echo -e "> 安装探针面板"

    # 探针面板文件夹
    if [ ! -d "${DASHBOARD_PATH}" ]; then
        mkdir -p $DASHBOARD_PATH
	else
        echo "您可能已经安装过面板端，重复安装会覆盖数据，请注意备份。"
        printf "是否退出安装? [Y/n] "
        read -r input
        case $input in
        [yY][eE][sS] | [yY])
            echo "退出安装"
            exit 0
            ;;
        [nN][oO] | [nN])
            echo "继续安装"
            ;;
        *)
            echo "退出安装"
            exit 0
            ;;
        esac
    fi
    
    chmod 777 -R $DASHBOARD_PATH

    command -v docker >/dev/null 2>&1
    if [[ $? != 0 ]]; then
        echo -e "正在安装 Docker"
        bash <(curl -sL https://${Get_Docker_URL}) ${Get_Docker_Argu} >/dev/null 2>&1
        if [[ $? != 0 ]]; then
            echo -e "${red}下载脚本失败，请检查本机能否连接 ${Get_Docker_URL}${plain}"
            return 0
        fi
        systemctl enable docker.service
        systemctl start docker.service
        echo -e "${green}Docker${plain} 安装成功"
    fi

    command -v docker-compose >/dev/null 2>&1
    if [[ $? != 0 ]]; then
        echo -e "正在安装 Docker Compose"
        wget -O /usr/local/bin/docker-compose "https://${GITHUB_URL}/docker/compose/releases/latest/download/docker-compose-$(uname -s)-$(uname -m)" >/dev/null 2>&1
        if [[ $? != 0 ]]; then
            echo -e "${red}下载脚本失败，请检查本机能否连接 ${GITHUB_URL}${plain}"
            return 0
        fi
        chmod +x /usr/local/bin/docker-compose
        echo -e "${green}Docker Compose${plain} 安装成功"
    fi

    modify_dashboard_config 0

    if [[ $# == 0 ]]; then
        before_show_menu
    fi
}

selinux() {
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
        version=$(curl -m 10 -sL "https://fastly.jsdelivr.net/gh/xos/serveragent/" | grep "option\.value" | awk -F "'" '{print $2}' | sed 's/nezhahq\/agent@/v/g')
    fi
    if [ ! -n "$version" ]; then
        version=$(curl -m 10 -sL "https://gcore.jsdelivr.net/gh/xos/serveragent/" | grep "option\.value" | awk -F "'" '{print $2}' | sed 's/xos\/serveragent@/v/g')
    fi

    if [ ! -n "$version" ]; then
        echo -e "获取版本号失败，请检查本机能否链接 https://api.github.com/repos/xos/serveragent/releases/latest"
        return 0
    else
        echo -e "当前最新版本为: ${version}"
    fi

    # 探针文件夹
    if [ ! -z "${AGENT_PATH}" ]; then
        mkdir -p $AGENT_PATH
        chmod 777 -R $AGENT_PATH
    fi
    echo "正在下载监控端"
    if [ -z "$CN" ]; then
        AGENT_URL="https://${GITHUB_URL}/xos/serveragent/releases/download/${version}/server-agent_linux_${os_arch}.zip"
    else
        AGENT_URL="https://${GITHUB_URL}/Ten/ServerAgent/releases/download/${version}/server-agent_linux_${os_arch}.zip"
    fi
    echo -e "正在下载探针"
    wget -t 2 -T 60 -O server-agent_linux_${os_arch}.zip $AGENT_URL >/dev/null 2>&1
    if [ $? != 0 ]; then
        err "Release 下载失败，请检查本机能否连接 ${GITHUB_URL}"
        return 1
    fi
    unzip -qo server-agent_linux_${os_arch}.zip &&
        mv server-agent $AGENT_PATH &&
        rm -rf server-agent_linux_${os_arch}.zip README.md

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
        version=$(curl -m 10 -sL "https://fastly.jsdelivr.net/gh/xos/serveragent/" | grep "option\.value" | awk -F "'" '{print $2}' | sed 's/xos\/serveragent@/v/g')
    fi
    if [ ! -n "$version" ]; then
        version=$(curl -m 10 -sL "https://gcore.jsdelivr.net/gh/xos/serveragent/" | grep "option\.value" | awk -F "'" '{print $2}' | sed 's/xos\/serveragent@/v/g')
    fi

    if [ ! -n "$version" ]; then
        echo -e "获取版本号失败，请检查本机能否链接 https://api.github.com/repos/xos/serveragent/releases/latest"
        return 0
    else
        echo -e "当前最新版本为: ${version}"
    fi

    # 探针文件夹
    if [ ! -z "${AGENT_PATH}" ]; then
        mkdir -p $AGENT_PATH
        chmod 777 -R $AGENT_PATH
    fi

    echo "正在下载监控端"
    if [ -z "$CN" ]; then
        AGENT_URL="https://${GITHUB_URL}/xos/serveragent/releases/download/${version}/server-agent_linux_${os_arch}.zip"
    else
        AGENT_URL="https://${GITHUB_URL}/Ten/ServerAgent/releases/download/${version}/server-agent_linux_${os_arch}.zip"
    fi
    echo -e "正在下载探针"
    wget -t 2 -T 60 -O server-agent_linux_${os_arch}.zip $AGENT_URL >/dev/null 2>&1
    if [ $? != 0 ]; then
        err "Release 下载失败，请检查本机能否连接 ${GITHUB_URL}"
        return 1
    fi
    unzip -qo server-agent_linux_${os_arch}.zip &&
        chmod +x server-agent &&
        mv server-agent $AGENT_PATH &&
        systemctl restart server-agent
        rm -rf server-agent_linux_${os_arch}.zip README.md

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
read_config(){
	[[ ! -e ${AGENT_SERVICE} ]] && echo -e "${red} 探针启动文件不存在 ! ${plain}" && exit 1
    	host=$(cat ${AGENT_SERVICE}|grep 'server-agent'|awk -F '-s' '{print $2}'|head -1|sed 's/\:/ /'|awk '{print $1}')
	port=$(cat ${AGENT_SERVICE}|grep 'server-agent'|awk -F '-s' '{print $2}'|head -1|sed 's/\:/ /'|awk '{print $2}')
	secret=$(cat ${AGENT_SERVICE}|grep 'p '|awk -F 'p ' '{print $NF}')
}
set_agent(){
    echo && echo -e "修改探针配置
    =========================
    ${green}1.${plain}  修改 域名
    ${green}2.${plain}  修改 端口
    ${green}3.${plain}  修改 密钥
    =========================
    ${green}4.${plain}  修改 全部配置" && echo
	    read -e -p "(默认: 取消):" modify
        [[ -z "${modify}" ]] && echo "已取消..." && exit 1

	if [[ "${modify}" == "1" ]]; then
        read_config
		set_host
        grpc_host=${grpc_host}
        sed -i "s/${host}/${grpc_host}/" ${AGENT_SERVICE}
        echo -e "探针域名 ${green}修改成功，请稍等重启生效${plain}"
        systemctl daemon-reload
        systemctl enable server-agent
        systemctl restart server-agent
        echo -e "探针 已重启完毕！"
        before_show_menu

	elif [[ "${modify}" == "2" ]]; then
        read_config
		set_port
        grpc_port=${grpc_port}
        sed -i "s/${port}/${grpc_port}/" ${AGENT_SERVICE}
        echo -e "探针端口${green} 修改成功，请稍等重启生效${plain}"
        systemctl daemon-reload
        systemctl enable server-agent
        systemctl restart server-agent
        echo -e "探针 已重启完毕！"
        before_show_menu

	elif [[ "${modify}" == "3" ]]; then
        read_config
		set_secret
        client_secret=${client_secret}
        sed -i "s/${secret}/${client_secret}/" ${AGENT_SERVICE}
        echo -e "探针密钥${green} 修改成功，请稍等重启生效${plain}"
        systemctl daemon-reload
        systemctl enable server-agent
        systemctl restart server-agent
        echo -e "探针 已重启完毕！"
        before_show_menu

	elif [[ "${modify}" == "4" ]]; then
		modify_agent_config
    else
		echo -e "${Error} 请输入正确的数字(1-4)" && exit 1
    fi
    sleep 3s
    start_menu
}

modify_agent_config() {
    echo -e "> 初始化探针配置"

    wget -t 2 -T 10 -O $AGENT_SERVICE https://${GITHUB_RAW_URL}/script/server-agent.service >/dev/null 2>&1
    if [[ $? != 0 ]]; then
        echo -e "${red}文件下载失败，请检查本机能否连接 ${GITHUB_RAW_URL}${plain}"
        return 0
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

    sed -i "s/grpc_host/${grpc_host}/" ${AGENT_SERVICE}
    sed -i "s/grpc_port/${grpc_port}/" ${AGENT_SERVICE}
    sed -i "s/client_secret/${client_secret}/" ${AGENT_SERVICE}

    shift 3
    if [ $# -gt 0 ]; then
        args=" $*"
        sed -i "/ExecStart/ s/$/${args}/" ${AGENT_SERVICE}
    fi

    echo -e "探针配置 ${green}修改成功，请稍等重启生效${plain}"

    systemctl daemon-reload
    systemctl enable server-agent
    systemctl restart server-agent

    if [[ $# == 0 ]]; then
        echo -e "探针 已重启完毕！"
        before_show_menu
    fi
}

modify_dashboard_config() {
    echo -e "> 修改探针面板配置"

    echo -e "正在下载 Docker 脚本"
    wget -t 2 -T 10 -O ${DASHBOARD_PATH}/docker-compose.yaml https://${GITHUB_RAW_URL}/script/docker-compose.yaml >/dev/null 2>&1
    if [[ $? != 0 ]]; then
        echo -e "${red}下载脚本失败，请检查本机能否连接 ${GITHUB_RAW_URL}${plain}"
        return 0
    fi

    mkdir -p $DASHBOARD_PATH/data

    wget -t 2 -T 10 -O ${DASHBOARD_PATH}/data/config.yaml https://${GITHUB_RAW_URL}/script/config.yaml >/dev/null 2>&1
    if [[ $? != 0 ]]; then
        echo -e "${red}下载脚本失败，请检查本机能否连接 ${GITHUB_RAW_URL}${plain}"
        return 0
    fi

    echo "关于 GitHub Oauth2 应用：在 https://github.com/settings/developers 创建，无需审核，Callback 填 http(s)://域名或IP/oauth2/callback" &&
        echo "关于 Gitee Oauth2 应用：在 https://gitee.com/oauth/applications 创建，无需审核，Callback 填 http(s)://域名或IP/oauth2/callback" &&
        read -ep "请输入 OAuth2 提供商(github/gitlab/jihulab/gitee，默认 github): " oauth2_type &&
        read -ep "请输入 Oauth2 应用的 Client ID: " github_oauth_client_id &&
        read -ep "请输入 Oauth2 应用的 Client Secret: " github_oauth_client_secret &&
        read -ep "请输入 GitHub/Gitee 登录名作为管理员，多个以逗号隔开: " admin_logins &&
        read -ep "请输入站点标题: " site_title &&
        read -ep "请输入站点访问端口（默认：8008）: " site_port &&
        read -ep "请输入用于探针接入的 GRPC 域名（必填，默认为空）: " grpc_host &&
        read -ep "请输入用于探针接入的 GRPC 端口（默认：2222）: " grpc_port

    if [[ -z "${admin_logins}" || -z "${github_oauth_client_id}" || -z "${github_oauth_client_secret}" || -z "${site_title}" ]]; then
        echo -e "${red}所有选项都不能为空${plain}"
        before_show_menu
        return 1
    fi

    if [[ -z "${site_port}" ]]; then
        site_port=8008
    fi
    if [[ -z "${grpc_host}" ]]; then
        grpc_host='grpc_host'
    fi
    if [[ -z "${grpc_port}" ]]; then
        grpc_port=2222
    fi
    if [[ -z "${oauth2_type}" ]]; then
        oauth2_type=github
    fi

    sed -i "s/oauth2_type/${oauth2_type}/" ${DASHBOARD_PATH}/data/config.yaml
    sed -i "s/admin_logins/${admin_logins}/" ${DASHBOARD_PATH}/data/config.yaml
    sed -i "s/site_port/${site_port}/g" ${DASHBOARD_PATH}/data/config.yaml
    sed -i "s/grpc_host/${grpc_host}/g" ${DASHBOARD_PATH}/data/config.yaml
    sed -i "s/grpc_port/${grpc_port}/g" ${DASHBOARD_PATH}/data/config.yaml
    sed -i "s/github_oauth_client_id/${github_oauth_client_id}/" ${DASHBOARD_PATH}/data/config.yaml
    sed -i "s/github_oauth_client_secret/${github_oauth_client_secret}/" ${DASHBOARD_PATH}/data/config.yaml
	sed -i "s/nz_language/zh-CN/" ${DASHBOARD_PATH}/data/config.yaml
    sed -i "s/site_title/${site_title}/g" ${DASHBOARD_PATH}/data/config.yaml
    sed -i "s/site_port/${site_port}/g" ${DASHBOARD_PATH}/docker-compose.yaml
    sed -i "s/grpc_port/${grpc_port}/g" ${DASHBOARD_PATH}/docker-compose.yaml
    sed -i "s/image_url/${Docker_IMG}/" ${DASHBOARD_PATH}/docker-compose.yaml

    echo -e "探针面板配置 ${green}修改成功，请稍等重启生效${plain}"

    restart_and_update

    if [[ $# == 0 ]]; then
        before_show_menu
    fi
}

restart_and_update() {
    echo -e "> 重启并更新探针面板"

    cd $DASHBOARD_PATH
    docker-compose pull
    docker-compose down
    docker-compose up -d
    if [[ $? == 0 ]]; then
        echo -e "${green}探针面板 重启成功${plain}"
        echo -e "默认管理面板地址：${yellow}域名:站点访问端口${plain}"
    else
        echo -e "${red}重启失败，可能是因为启动时间超过了两秒，请稍后查看日志信息${plain}"
    fi

    if [[ $# == 0 ]]; then
        before_show_menu
    fi
}

start_dashboard() {
    echo -e "> 启动 探针面板"

    cd $DASHBOARD_PATH && docker-compose up -d
    if [[ $? == 0 ]]; then
        echo -e "${green}探针面板 启动成功${plain}"
    else
        echo -e "${red}启动失败，请稍后查看日志信息${plain}"
    fi

    if [[ $# == 0 ]]; then
        before_show_menu
    fi
}

stop_dashboard() {
    echo -e "> 停止 探针面板"

    cd $DASHBOARD_PATH && docker-compose down
    if [[ $? == 0 ]]; then
        echo -e "${green}探针面板 停止成功${plain}"
    else
        echo -e "${red}停止失败，请稍后查看日志信息${plain}"
    fi

    if [[ $# == 0 ]]; then
        before_show_menu
    fi
}

show_dashboard_log() {
    echo -e "> 获取探针面板日志"

    cd $DASHBOARD_PATH && docker-compose logs -f

    if [[ $# == 0 ]]; then
        before_show_menu
    fi
}

uninstall_dashboard() {
    echo -e "> 卸载 探针面板"

    cd $DASHBOARD_PATH &&
        docker-compose down
    rm -rf $DASHBOARD_PATH
    docker rmi -f ghcr.io/xos/server-dash > /dev/null 2>&1
    docker rmi -f registry.cn-shanghai.aliyuncs.com/dns/server-dash > /dev/null 2>&1
    clean_all

    if [[ $# == 0 ]]; then
        before_show_menu
    fi
}

show_agent_log() {
    echo -e "> 获取探针日志"

    journalctl -xf -u server-agent.service

    if [[ $# == 0 ]]; then
        before_show_menu
    fi
}

uninstall_agent() {
    echo -e "> 卸载 探针"

    systemctl disable server-agent.service
    systemctl stop server-agent.service
    rm -rf $AGENT_SERVICE
    systemctl daemon-reload

    rm -rf $AGENT_PATH
    clean_all

    if [[ $# == 0 ]]; then
        before_show_menu
    fi
}

restart_agent() {
    echo -e "> 重启 探针"

    systemctl restart server-agent.service

    if [[ $# == 0 ]]; then
        before_show_menu
    fi
}

clean_all() {
    if [ -z "$(ls -A ${BASE_PATH})" ]; then
        rm -rf ${BASE_PATH}
    fi
}

show_usage() {
    echo "探针 管理脚本使用方法: "
    echo "--------------------------------------------------------"
    echo "./server-status.sh                            - 显示管理菜单"
    echo "./server-status.sh install_dashboard          - 安装面板端"
    echo "./server-status.sh modify_dashboard_config    - 修改面板配置"
    echo "./server-status.sh start_dashboard            - 启动探针面板"
    echo "./server-status.sh stop_dashboard             - 停止探针面板"
    echo "./server-status.sh restart_and_update         - 重启并更新面板"
    echo "./server-status.sh show_dashboard_log         - 查看面板日志"
    echo "./server-status.sh uninstall_dashboard        - 卸载管理面板"
    echo "--------------------------------------------------------"
    echo "./server-status.sh install_agent              - 安装探针"
    echo "./server-status.sh update_agent               - 更新探针"
    echo "./server-status.sh modify_agent_config        - 修改探针配置"
    echo "./server-status.sh show_agent_log             - 探针状态"
    echo "./server-status.sh uninstall_agent            - 卸载探针"
    echo "./server-status.sh restart_agent              - 重启探针"
    echo "./server-status.sh update_script              - 更新脚本"
    echo "--------------------------------------------------------"
}

show_menu() {
    clear
    echo -e "
    =========================
    ${green}探针管理脚本${plain} ${red}[${VERSION}]${plain}
    =========================
    ${green}1.${plain} 安装探针面板
    ${green}2.${plain} 修改探针面板配置
    ${green}3.${plain} 启动探针面板
    ${green}4.${plain} 停止探针面板
    ${green}5.${plain} 重启并更新探针面板
    ${green}6.${plain} 查看探针面板日志
    ${green}7.${plain} 卸载管理探针面板
    —————————————————————————
    ${green}8.${plain} 安装 探针
    ${green}9.${plain} 更新 探针
    ${green}10.${plain} 探针 状态
    ${green}11.${plain} 卸载 探针
    ${green}12.${plain} 重启 探针
    —————————————————————————
    ${green}13.${plain} 修改探针配置
    —————————————————————————
    ${green}0.${plain} 更新脚本
    ${green}00.${plain} 退出脚本
    =========================
    "
    echo && read -ep "请输入选择 [0-13]: " num

    case "${num}" in
    00)
        exit 0
        ;;
    1)
        install_dashboard
        ;;
    2)
        modify_dashboard_config
        ;;
    3)
        start_dashboard
        ;;
    4)
        stop_dashboard
        ;;
    5)
        restart_and_update
        ;;
    6)
        show_dashboard_log
        ;;
    7)
        uninstall_dashboard
        ;;
    8)
        install_agent
        ;;
    9)
        update_agent
        ;;
    10)
        show_agent_log
        ;;
    11)
        uninstall_agent
        ;;
    12)
        restart_agent
        ;;
    13)
        set_agent
        ;;
    0)
        update_script
        ;;
    *)
        echo -e "${red}请输入正确的数字 [0-22]${plain}"
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
    *) show_usage ;;
    esac
else
    show_menu
fi
