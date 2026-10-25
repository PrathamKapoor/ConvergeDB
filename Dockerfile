# --- web build ---
FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# --- go build ---
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -o /out/convergedb ./cmd/convergedb

# --- runtime ---
FROM alpine:3.20
RUN adduser -D convergedb
WORKDIR /data
COPY --from=build /out/convergedb /usr/local/bin/convergedb
COPY --from=web /src/web/dist /opt/convergedb-web
USER convergedb
EXPOSE 8101 8102 8103
ENTRYPOINT ["/usr/local/bin/convergedb"]
CMD ["serve", "--id", "alpha", "--db", "/data/alpha.db", "--http", "127.0.0.1:8101"]
