FROM python:3.13-alpine
RUN apk add --no-cache bash curl git ca-certificates && adduser -D -u 1000 agent
COPY --chmod=0755 background.py /usr/local/bin/kit-tck-background
USER agent
WORKDIR /home/agent
CMD ["sleep", "infinity"]
