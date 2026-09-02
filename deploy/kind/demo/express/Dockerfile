FROM node:22-bookworm-slim
WORKDIR /app
COPY package.json ./
RUN npm install --omit=dev=false
COPY otel.js server.js log.js ./
ENV NODE_ENV=production
EXPOSE 3000 9464
CMD ["node", "server.js"]
