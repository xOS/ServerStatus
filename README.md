# 探针轻量版
> 本项目为原项目[哪吒探针](https://github.com/naiba/nezha)的精简修改自用版

![GitHub Workflow Status](https://img.shields.io/github/workflow/status/xOS/ServerStatus/Dashboard%20image?label=管理面板%20v0.1.15&logo=github&style=for-the-badge) ![Agent release](https://img.shields.io/github/v/release/xOS/ServerStatus?color=brightgreen&label=Agent&style=for-the-badge&logo=github) ![GitHub Workflow Status](https://img.shields.io/github/workflow/status/xOS/ServerStatus/Agent%20release?label=Agent%20CI&logo=github&style=for-the-badge) ![shell](https://img.shields.io/badge/安装脚本-v0.1.6-brightgreen?style=for-the-badge&logo=linux)

## 注意：

* 本项目与原项目不兼容！
* 本项目配置文件与原项目不通用！

## 精简掉的功能：

1. 远程脚本执行；
2. 远程定时任务；
3. 网站监测，包含SSL证书监测；
4. 周期流量统计。
5. ......

## 演示图

![首页截图](https://i.cdn.ink/views/e6c1b8.png)

## 安装脚本

**推荐配置：** 安装前解析 _两个域名_ 到面板服务器，一个作为 _公开访问_ ，可以 **接入CDN**；另外一个作为安装探针时连接面板使用，**不能接入CDN** 直接暴露面板主机IP。

```shell
curl -L https://raw.githubusercontent.com/xos/serverstatus/master/script/server-status.sh -o server-status.sh && chmod +x server-status.sh
sudo ./server-status.sh
```

<details>
    <summary>国内镜像加速：</summary>

```shell
curl -L https://cdn.jsdelivr.net/gh/xos/serverstatus@master/script/server-status.sh -o server-status.sh && chmod +x server-status.sh
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

### 探针 Agent 自定义

#### 自定义监控的网卡和硬盘分区

执行 `/opt/server-status/agent/server-agent --edit-agent-config` 来选择自定义的网卡和分区，然后重启探针即可

#### 运行参数

通过执行 `./server-agent --help` 查看支持的参数，如果你使用一键脚本，可以编辑 `/etc/systemd/system/server-agent.service`，在 `ExecStart=` 这一行的末尾加上

- `--report-delay` 系统信息上报的间隔，默认为 1 秒，可以设置为 3 来进一步降低探针端系统资源占用（配置区间 1-4）
- `--skip-conn` 不监控连接数，机场/连接密集型机器推荐设置，不然比较占 CPU([shirou/gopsutil/issues#220](https://github.com/shirou/gopsutil/issues/220))
- `--skip-procs` 不监控进程数，也可以降低探针占用
- `--disable-auto-update` 禁止 **自动更新探针**（安全特性）
- `--disable-force-update` 禁止 **强制更新探针**（安全特性）
- `--disable-command-execute` 禁止在探针机器上执行定时任务、打开在线终端（安全特性）
- `--tls` 启用 SSL/TLS 加密（使用 nginx 反向代理探针的 grpc 连接，并且 nginx 开启 SSL/TLS 时，需要启用该项配置）

## 功能说明

<details>
    <summary>报警通知：负载、CPU、内存、硬盘、带宽、流量、进程数、连接数实时监控。</summary>
#### 灵活通知方式

`#NG#` 是面板消息占位符，面板触发通知时会自动替换占位符到实际消息

Body 内容是`JSON` 格式的：**当请求类型为 FORM 时**，值为 `key:value` 的形式，`value` 里面可放置占位符，通知时会自动替换。**当请求类型为 JSON 时** 只会简进行字符串替换后直接提交到`URL`。

URL 里面也可放置占位符，请求时会进行简单的字符串替换。

参考下方的示例，非常灵活。

1. 添加通知方式

   - server 酱示例

     - 名称：server 酱
     - URL：<https://sc.ftqq.com/SCUrandomkeys.send?text=#NG>#
     - 请求方式: GET
     - 请求类型: 默认
     - Body: 空

   - wxpusher 示例，需要关注你的应用

     - 名称: wxpusher
     - URL：<http://wxpusher.zjiecode.com/api/send/message>
     - 请求方式: POST
     - 请求类型: JSON
     - Body: `{"appToken":"你的appToken","topicIds":[],"content":"#NG#","contentType":"1","uids":["你的uid"]}`

   - telegram 示例 [@haitau](https://github.com/haitau) 贡献

     - 名称：telegram 机器人消息通知
     - URL：<https://api.telegram.org/botXXXXXX/sendMessage?chat_id=YYYYYY&text=#NG>#
     - 请求方式: GET
     - 请求类型: 默认
     - Body: 空
     - URL 参数获取说明：botXXXXXX 中的 XXXXXX 是在 telegram 中关注官方 @Botfather ，输入/newbot ，创建新的机器人（bot）时，会提供的 token（在提示 Use this token to access the HTTP API:后面一行）这里 'bot' 三个字母不可少。创建 bot 后，需要先在 telegram 中与 BOT 进行对话（随便发个消息），然后才可用 API 发送消息。YYYYYY 是 telegram 用户的数字 ID。与机器人@userinfobot 对话可获得。

2. 添加一个离线报警

   - 名称：离线通知
   - 规则：`[{"Type":"offline","Duration":10}]`
   - 启用：√

3. 添加一个监控 CPU 持续 10s 超过 50% **且** 内存持续 20s 占用低于 20% 的报警

   - 名称：CPU+内存
   - 规则：`[{"Type":"cpu","Min":0,"Max":50,"Duration":10},{"Type":"memory","Min":20,"Max":0,"Duration":20}]`
   - 启用：√

#### 报警规则说明

##### 基本规则

- type
  - `cpu`、`memory`、`swap`、`disk`
  - `net_in_speed` 入站网速、`net_out_speed` 出站网速、`net_all_speed` 双向网速、`transfer_in` 入站流量、`transfer_out` 出站流量、`transfer_all` 双向流量
  - `offline` 离线监控
  - `load1`、`load5`、`load15` 负载
  - `process_count` 进程数 _目前取线程数占用资源太多，暂时不支持_
  - `tcp_conn_count`、`udp_conn_count` 连接数
- duration：持续秒数，秒数内采样记录 30% 以上触发阈值才会报警（防数据插针）
- min/max
  - 流量、网速类数值 为字节（1KB=1024B，1MB = 1024\*1024B）
  - 内存、硬盘、CPU 为占用百分比
  - 离线监控无需设置
- cover `[{"type":"offline","duration":10, "cover":0, "ignore":{"5": true}}]`
  - `0` 监控所有，通过 `ignore` 忽略特定服务器
  - `1` 忽略所有，通过 `ignore` 监控特定服务器
- ignore: `{"1": true, "2":false}` 特定服务器，搭配 `cover` 使用

</details>

<details>
  <summary>自定义代码：改LOGO、改色调、加统计代码等。</summary>

- 默认主题更改进度条颜色示例

  ```html
  <style>
  .ui.fine.progress> .bar {
      background-color: pink !important;
  }
  </style>
  ```

- 默认主题修改 LOGO、修改页脚示例（来自 [@iLay1678](https://github.com/iLay1678)）

  ```html
  <style>
  .right.menu>a{
  visibility: hidden;
  }
  .footer .is-size-7{
  visibility: hidden;
  }
  .item img{
  visibility: hidden;
  }
  </style>
  <script>
  window.onload = function(){
  var avatar=document.querySelector(".item img")
  var footer=document.querySelector("div.is-size-7")
  footer.innerHTML="Powered by 你的名字"
  footer.style.visibility="visible"
  avatar.src="你的方形logo地址"
  avatar.style.visibility="visible"
  }
  </script>
  ```

</details>

## 常见问题


<details>
    <summary>如何进行数据迁移、备份恢复？</summary>

1. 先使用一键脚本 `停止面板`
2. 打包 `/opt/server-status/dashboard` 文件夹，到新环境相同位置
3. 使用一键脚本 `启动面板`

</details>

<details>
    <summary>探针启动/上线 问题自检流程</summary>


1. 直接执行 `/opt/server-status/agent/server-agent -s 面板IP或非CDN域名:面板GRPC端口 -p 探针密钥 -d` 查看日志是否是 DNS 问题。
2. `nc -v 域名/IP 面板GRPC端口` 或者 `telnet 域名/IP 面板GRPC端口` 检验是否是网络问题，检查本机与面板服务器出入站防火墙，如果单机无法判断可借助 https://port.ping.pe/ 提供的端口检查工具进行检测。
3. 如果上面步骤检测正常，探针正常上线，尝试关闭 SELinux，[如何关闭 SELinux？](https://www.google.com/search?q=%E5%85%B3%E9%97%ADSELINUX)
</details>

<details>
    <summary>如何使 旧版OpenWRT/LEDE 自启动？</summary>

参考此项目: <https://github.com/Erope/openwrt_nezha>

</details>

<details>
    <summary>如何使 新版OpenWRT 自启动？来自 @艾斯德斯</summary>

首先在 release 下载对应的二进制解压 tar.gz 包后放置到 `/root`，然后 `chmod +x /root/server-agent` 赋予执行权限，然后创建 `/etc/init.d/server-status-service`：

```shell
#!/bin/sh /etc/rc.common

START=99
USE_PROCD=1

start_service() {
	procd_open_instance
	procd_set_param command /root/server-agent -s 面板域名:接收端口 -p 唯一秘钥 -d
	procd_set_param respawn
	procd_close_instance
}

stop_service() {
    killall server-agent
}

restart() {
 stop
 sleep 2
 start
}
```

赋予执行权限 `chmod +x /etc/init.d/server-status-service` 然后启动服务 `/etc/init.d/server-status-service enable && /etc/init.d/server-status-service start`

</details>

<details>
    <summary>实时通道断开/在线终端连接失败</summary>

使用反向代理时需要针对 `/ws`,`/terminal` 路径的 WebSocket 进行特别配置以支持实时更新服务器状态和 **WebSSH**。

- Nginx(宝塔)：在你的 nginx 配置文件中加入以下代码

  ```nginx
  server{

      #原有的一些配置
      #server_name blablabla...

      location ~ ^/(ws|terminal/.+)$  {
          proxy_pass http://ip:站点访问端口;
          proxy_set_header Upgrade $http_upgrade;
          proxy_set_header Connection "Upgrade";
          proxy_set_header Host $host;
      }

      #其他的 location blablabla...
  }
  ```

  如果非宝塔，还要在 `server{}` 中添加上这一段

  ```nginx
  location / {
    proxy_pass http://ip:站点访问端口;
    proxy_set_header Host $host;
  }
  ```

- CaddyServer v1（v2 无需特别配置）

  ```Caddyfile
  proxy /ws http://ip:8008 {
      websocket
  }
  proxy /terminal/* http://ip:8008 {
      websocket
  }
  ```

</details>

<details>
    <summary>反向代理 gRPC 端口（支持 Cloudflare CDN）</summary>
使用 Nginx 或者 Caddy 反向代理 gRPC

- Nginx 配置

```nginx
server {
    listen 443 ssl http2;
    listen [::]:443 ssl http2;
    server_name ip-to-dashboard.nai.ba; # 你的探针连接面板的域名

    ssl_certificate          /data/letsencrypt/fullchain.pem; # 你的域名证书路径
    ssl_certificate_key      /data/letsencrypt/key.pem;       # 你的域名私钥路径

    underscores_in_headers on;

    location / {
        grpc_read_timeout 300s;
        grpc_send_timeout 300s;
        grpc_pass grpc://localhost:5555;
    }
}
```

- Caddy 配置

```Caddyfile
ip-to-dashboard.nai.ba:443 { # 你的探针连接面板的域名
    reverse_proxy {
        to localhost:5555
        transport http {
            versions h2c 2
        }
    }
}
```


面板端配置

- 首先登录面板进入管理后台 打开设置页面，在 `未接入CDN的面板服务器域名/IP` 中填入上一步在 Nginx 或 Caddy 中配置的域名 比如 `ip-to-dashboard.nai.ba` ，并保存。
- 然后在面板服务器中，打开 /opt/server-status/dashboard/data/config.yaml 文件，将 `proxygrpcport` 修改为 Nginx 或 Caddy 监听的端口，比如上一步设置的 `443` ；因为我们在 Nginx 或 Caddy 中开启了 SSL/TLS，所以需要将 `tls` 设置为 `true` ；修改完成后重启面板。


探针端配置

- 登录面板管理后台，复制一键安装命令，在对应的服务器上面执行一键安装命令重新安装探针端即可。


开启 Cloudflare CDN（可选）

根据 Cloudflare gRPC 的要求：gRPC 服务必须侦听 443 端口 且必须支持 TLS 和 HTTP/2。
所以如果需要开启 CDN，必须在配置 Nginx 或者 Caddy 反向代理 gRPC 时使用 443 端口，并配置证书（Caddy 会自动申请并配置证书）。

- 登录 Cloudflare，选择使用的域名。打开 `网络` 选项将 `gRPC` 开关打开，打开 `DNS` 选项，找到 Nginx 或 Caddy 反代 gRPC 配置的域名的解析记录，打开橙色云启用CDN。

</details>
