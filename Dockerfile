# 多阶段构建 - 证书和工具阶段
FROM alpine:3.19 AS certs
RUN apk update && apk add --no-cache ca-certificates tzdata busybox-static

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

# 复制基本的 shell 工具（用于健康检查和脚本执行）
COPY --from=certs /bin/busybox /bin/sh
COPY --from=certs /bin/busybox /bin/mkdir
COPY --from=certs /bin/busybox /bin/chmod
COPY --from=certs /bin/busybox /bin/cat
COPY --from=certs /bin/busybox /bin/echo
COPY --from=certs /bin/busybox /bin/date
COPY --from=certs /bin/busybox /bin/pgrep

# 复制入口脚本和应用
COPY ./script/entrypoint.sh /entrypoint.sh
COPY dist/server-dash-${TARGETOS}-${TARGETARCH} /dashboard/app

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