FROM node:22-alpine AS frontend
WORKDIR /src/coobook_front-end
COPY coobook_front-end/package.json coobook_front-end/package-lock.json ./
RUN npm ci
COPY coobook_front-end/ ./
RUN npm run build

FROM golang:1.25-alpine AS backend
WORKDIR /src/cookbook_back-end
COPY cookbook_back-end/go.mod cookbook_back-end/go.sum ./
RUN go mod download
COPY cookbook_back-end/ ./
COPY --from=frontend /src/coobook_front-end/dist ./web/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/cookbook .

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata wget && addgroup -S cookbook && adduser -S -G cookbook cookbook && mkdir -p /data && chown cookbook:cookbook /data
COPY --from=backend /out/cookbook /usr/local/bin/cookbook
USER cookbook
ENV COOKBOOK_ADDR=0.0.0.0:8000 COOKBOOK_DATABASE=/data/cookbook.db
EXPOSE 8000
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 CMD wget -qO- http://127.0.0.1:8000/healthz >/dev/null || exit 1
ENTRYPOINT ["/usr/local/bin/cookbook"]
