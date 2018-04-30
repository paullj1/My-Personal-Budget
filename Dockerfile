# HackTheArch Dockerfile
# VERSION 1.0

FROM ruby:2.5.1
MAINTAINER Paul Jordan <paullj1@gmail.com>

HEALTHCHECK --interval=5m --timeout=3s \
  CMD curl -kf https://127.0.0.1/ || exit 1

ARG secret

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        build-essential \
        nodejs \
        postgresql-client \
    && rm -rf /var/lib/apt/lists/*

RUN mkdir /usr/src/mpb
WORKDIR /usr/src/mpb
ADD Gemfile Gemfile.lock /usr/src/mpb/
RUN bundle install
ADD . /usr/src/mpb/
RUN chown -R $USER:$USER .

EXPOSE 3000
