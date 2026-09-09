FROM dunglas/frankenphp:1-php8.4

WORKDIR /app

RUN install-php-extensions pdo_pgsql intl opcache @composer

ENV COMPOSER_ALLOW_SUPERUSER=1
COPY composer.json composer.lock ./
RUN composer install --no-dev --no-scripts --no-autoloader --prefer-dist

COPY bin bin
COPY config config
COPY public public
COPY src src
COPY docker/Caddyfile /etc/caddy/Caddyfile
COPY docker/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh bin/console \
 && mkdir -p var/cache var/log \
 && composer dump-autoload --no-dev --optimize

ENV SERVER_NAME=:80
ENV FRANKENPHP_CONFIG="worker ./public/index.php"
ENV APP_RUNTIME=Runtime\\FrankenPhpSymfony\\Runtime
ENV APP_ENV=prod
ENV APP_DEBUG=0

EXPOSE 80
ENTRYPOINT ["entrypoint.sh"]
CMD ["frankenphp", "run", "--config", "/etc/caddy/Caddyfile"]
