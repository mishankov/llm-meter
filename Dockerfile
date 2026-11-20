FROM --platform=$BUILDPLATFORM node:24-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
COPY --from=web /src/web/build ./web/build
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -tags embedui -trimpath -ldflags='-s -w' -o /llm-proxy .

FROM alpine:3.23
RUN apk add --no-cache ca-certificates && addgroup -S -g 10001 llmproxy && \
    adduser -S -u 10001 -G llmproxy llmproxy && mkdir /data && chown llmproxy:llmproxy /data
COPY --from=build /llm-proxy /usr/local/bin/llm-proxy
USER llmproxy
ENV DATA_DIR=/data
EXPOSE 8080
VOLUME ["/data"]
ENTRYPOINT ["/usr/local/bin/llm-proxy"]
