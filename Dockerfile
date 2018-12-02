# MyPersonalBudget Dockerfile
# VERSION 1.0

FROM ruby:2.5.1-alpine3.7
MAINTAINER Paul Jordan <paullj1@gmail.com>

HEALTHCHECK --interval=5m --timeout=3s \
  CMD curl -kf http://127.0.0.1:3000/ || exit 1

RUN apk update && apk add \
        build-base \
        nodejs \
        libpq \
        curl \
        curl-dev \
        postgresql \
        libxml2-dev \
        postgresql-dev \
        postgresql-client


RUN mkdir /usr/src/mpb
WORKDIR /usr/src/mpb
ADD Gemfile Gemfile.lock /usr/src/mpb/
RUN gem install bundler && bundle install 
ADD . /usr/src/mpb/

CMD [ "/bin/sh", "/usr/src/mpb/docker-entrypoint.sh" ]
EXPOSE 3000
