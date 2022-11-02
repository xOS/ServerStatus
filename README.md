# 探针

> 本项目为原项目[哪吒探针](https://github.com/naiba/nezha)的精简修改自用版

![GitHub Workflow Status](https://img.shields.io/github/workflow/status/xOS/ServerStatus/Dashboard%20image?label=管理面板%20v0.1.30&logo=github&style=for-the-badge) ![Agent release](https://img.shields.io/github/v/release/xOS/ServerStatus?color=brightgreen&label=Agent&style=for-the-badge&logo=github) ![GitHub Workflow Status](https://img.shields.io/github/workflow/status/xOS/ServerStatus/Agent%20release?label=Agent%20CI&logo=github&style=for-the-badge) ![shell](https://img.shields.io/badge/安装脚本-v0.1.10-brightgreen?style=for-the-badge&logo=linux)

## 注意：

* 本项目与原项目不兼容！
* 本项目配置文件与原项目不通用！

## 精简掉的功能：

1. 网站监测，包含SSL证书监测；
2. ......

## 演示图

### 前台

![首页截图](https://i.cdn.ink/views/2022/05/25/b168b1.png)

### 后台

![后台截图](https://i.cdn.ink/views/2022/05/25/fd1b7d.png)

## 安装脚本

```shell
curl -L https://raw.githubusercontent.com/xos/serverstatus/master/script/server-status.sh -o server-status.sh && chmod +x server-status.sh
sudo ./server-status.sh
```

<details>
    <summary>国内镜像加速：</summary>

```shell
curl -L https://fastly.jsdelivr.net/gh/xos/serverstatus@master/script/server-status.sh -o server-status.sh && chmod +x server-status.sh
CN=true sudo ./server-status.sh
```

</details>

_\* 使用 WatchTower 可以自动更新面板，Windows 终端可以使用 nssm 配置自启动。_



## 非Docker环境手动部署控制面板

注意：

* 需要安装`Golang`且版本需要1.18或以上。
* 默认安装路径 `/opt/server-status/dashboard`。
* 手动部署的面板暂无法通过脚本进行面板部分的控制操作。

1.克隆仓库

```bash
git clone https://github.com/xOS/ServerStatus.git
```

2.下载依赖

```bash
cd ServerStatus/
go mod tidy -v
```

3.编译，以`AMD64`架构为例

```bash
cd cmd/dashboard/
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -o server-dash -ldflags="-s -w"
```

4.部署面板为系统服务

```bash
mkdir -p /opt/server-status/dashboard
mv server-dash /opt/server-status/dashboard/
cd ../..
cp resource/ /opt/server-status/dashboard/ -r
mkdir -p /opt/server-status/dashboard/data
cp script/config.yaml /opt/server-status/dashboard/data
cp script/server-dash.service /etc/systemd/system
```

5.修改配置文件`/opt/server-status/dashboard/data/config.yaml`，注册服务并启动

```bash
systemctl enable server-dash
systemctl start server-dash
```
## 通知方式

`#NG#` 是面板消息占位符，面板触发通知时会自动替换占位符到实际消息

Body 内容是`JSON` 格式的：**当请求类型为 FORM 时**，值为 `key:value` 的形式，`value` 里面可放置占位符，通知时会自动替换。**当请求类型为 JSON 时** 只会简进行字符串替换后直接提交到`URL`。

URL 里面也可放置占位符，请求时会进行简单的字符串替换。

参考下方的示例，非常灵活。

### 添加通知方式

   - server 酱示例

     - 名称：server 酱
     - URL：`https://sc.ftqq.com/SCUrandomkeys.send?text=#NG#`
     - 请求方式: `GET`
     - 请求类型: 默认
     - Body: 空

   - wxpusher 示例，需要关注你的应用

     - 名称: wxpusher
     - URL：`http://wxpusher.zjiecode.com/api/send/message`
     - 请求方式: `POST`
     - 请求类型: `JSON`
     - Body: `{"appToken":"你的appToken","topicIds":[],"content":"#NG#","contentType":"1","uids":["你的uid"]}`

   - telegram 示例 [@haitau](https://github.com/haitau) 贡献

     - 名称：telegram 机器人消息通知
     - URL：`https://api.telegram.org/botXXXXXX/sendMessage?chat_id=YYYYYY&text=#NG#`
     - 请求方式: `GET`
     - 请求类型: 默认
     - Body: 空
     - URL 参数获取说明：botXXXXXX 中的 XXXXXX 是在 telegram 中关注官方 @Botfather ，输入/newbot ，创建新的机器人（bot）时，会提供的 token（在提示 Use this token to access the HTTP API:后面一行）这里 'bot' 三个字母不可少。创建 bot 后，需要先在 telegram 中与 BOT 进行对话（随便发个消息），然后才可用 API 发送消息。YYYYYY 是 telegram 用户的数字 ID。与机器人@userinfobot 对话可获得。
