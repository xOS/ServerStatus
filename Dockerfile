# 多阶段构建 - 证书和工具阶段
FROM alpine:3.19 AS certs
RUN apk update && apk add --no-cache ca-certificates tzdata busybox-static

# 脚本准备阶段
FROM alpine:3.19 AS script-prep
WORKDIR /prep
# 复制并设置entrypoint.sh权限
COPY script/entrypoint.sh ./entrypoint.sh
# 规范化换行并赋予可执行权限（避免 CRLF 导致的执行失败）
RUN sed -i 's/\r$//' ./entrypoint.sh && chmod +x ./entrypoint.sh

# 二进制文件准备阶段
FROM alpine:3.19 AS binary-prep
ARG TARGETARCH
WORKDIR /prep

# 复制所有构建产物
COPY dist/ ./dist/

# 查找并复制正确的二进制文件
RUN find ./dist -name "*linux*${TARGETARCH}*" -type f -executable | head -1 | xargs -I {} cp {} /prep/app || \
    find ./dist -name "*${TARGETARCH}*" -type f -executable | head -1 | xargs -I {} cp {} /prep/app || \
    find ./dist -name "server-dash*" -type f -executable | head -1 | xargs -I {} cp {} /prep/app

# 设置执行权限
RUN test -f /prep/app && chmod +x /prep/app

# 最终运行阶段
FROM scratch

ARG TARGETOS
ARG TARGETARCH
ARG TZ=Asia/Shanghai

# 从证书阶段复制必要文件
COPY --from=certs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=certs /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=certs /etc/passwd /etc/passwd
COPY --from=certs /etc/group /etc/group

# 复制静态 busybox 到 scratch（重要：避免动态链接导致的“no such file or directory”）
# 注意：busybox-static 包提供的是 /bin/busybox.static
COPY --from=certs /bin/busybox.static /bin/sh
COPY --from=certs /bin/busybox.static /bin/busybox
COPY --from=certs /bin/busybox.static /bin/mkdir
COPY --from=certs /bin/busybox.static /bin/chmod
COPY --from=certs /bin/busybox.static /bin/cat
COPY --from=certs /bin/busybox.static /bin/echo
COPY --from=certs /bin/busybox.static /bin/date
COPY --from=certs /bin/busybox.static /bin/pgrep
COPY --from=certs /bin/busybox.static /bin/test
COPY --from=certs /bin/busybox.static /bin/ls

# 复制入口脚本和应用
COPY --from=script-prep /prep/entrypoint.sh /entrypoint.sh
COPY --from=binary-prep /prep/app /dashboard/app

# 复制静态资源文件（重要：应用依赖这些文件）
COPY resource/ /dashboard/resource/

# 设置工作目录和环境变量
WORKDIR /dashboard
ENV TZ=$TZ
ENV GIN_MODE=release
ENV PATH=/bin

# 创建数据目录并设置权限
VOLUME ["/dashboard/data"]

# 暴露端口
EXPOSE 80 2222

# 健康检查
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD ["/dashboard/app", "--health-check"] || exit 1

# 使用入口脚本启动
ENTRYPOINT ["/entrypoint.sh"]