FROM node:24-alpine AS assets
WORKDIR /src
COPY package*.json ./
RUN npm ci --ignore-scripts
COPY scripts/ scripts/
RUN npm run vendor

FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=assets /src/web/vendor/ web/vendor/
ARG REVISION=unknown
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /gateway .

FROM gcr.io/distroless/static-debian13:nonroot
ARG REVISION=unknown
LABEL org.opencontainers.image.source="https://github.com/vadipp/piped-sharing-gateway" \
      org.opencontainers.image.revision=$REVISION
COPY --from=build /gateway /gateway
USER 65532:65532
EXPOSE 8080 8081
HEALTHCHECK --interval=30s --timeout=5s CMD ["/gateway", "healthcheck"]
ENTRYPOINT ["/gateway"]
