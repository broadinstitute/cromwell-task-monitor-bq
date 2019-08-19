FROM golang:alpine AS build

WORKDIR /opt

RUN apk add git

ADD go.* ./
RUN go mod download

ADD  *.go ./
RUN go build


FROM alpine

ARG USER=monitor

RUN apk add --no-cache ca-certificates && \
    adduser -SH ${USER}

COPY --from=build /opt/monitor /usr/local/bin

USER ${USER}

ENTRYPOINT ["monitor"]
