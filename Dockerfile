# The binary is cross-compiled on the build machine for the target, so
# only the small runtime stage runs under emulation.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS TARGETARCH TARGETVARIANT
# The go tool reads the version from the checkout, through git.
RUN apk add --no-cache git
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOARM=${TARGETVARIANT#v} \
	go build -trimpath -ldflags "-s -w" -o /alborz ./cmd/alborz

FROM alpine:3
# Calendars name their zones; Alpine ships none.
RUN apk add --no-cache tzdata \
	&& adduser -D -H -u 10001 alborz \
	&& mkdir /data /cache \
	&& chown alborz /data /cache
COPY --from=build /alborz /usr/local/bin/alborz
USER alborz
EXPOSE 1323
# It refuses to start without LBRZ_LOGIN_KEY; without a volume on /data
# it keeps its data inside the container.
ENV XDG_CACHE_HOME=/cache
ENTRYPOINT ["alborz", "-data-dir", "/data"]
