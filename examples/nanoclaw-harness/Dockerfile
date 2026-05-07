FROM node:22-slim

WORKDIR /app

# Install build essentials if needed, but better-sqlite3 might need them
RUN apt-get update && apt-get install -y \
    python3 \
    make \
    g++ \
    && rm -rf /var/lib/apt/lists/*

COPY package.json ./
# We might not have package-lock.json if pnpm was used, but we have pnpm-lock.yaml
# Let's just run npm install and let it generate or use what it finds.
# To be safe with better-sqlite3, we install deps.

RUN npm install

COPY . .

RUN npm run build

# Expose ports for webhook and callback
EXPOSE 3000 3001

CMD ["npm", "start"]
