FROM alpine AS certs
RUN apk update && apk add ca-certificates

FROM busybox:stable-musl

ARG TARGETOS
ARG TARGETARCH

COPY --from=certs /etc/ssl/certs /etc/ssl/certs
COPY ./script/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

WORKDIR /dashboard
COPY dist/server-dash-${TARGETOS}-${TARGETARCH} ./app

VOLUME ["/dashboard/data"]
EXPOSE 80 2222
ARG TZ=Asia/Shanghai
ENV TZ=$TZ
ENTRYPOINT ["/entrypoint.sh"]