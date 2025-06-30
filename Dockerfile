# Temel imaj olarak Node.js'in alpine (küçük) sürümünü kullan.
FROM node:20-alpine

# Container içinde uygulamanın çalışacağı /app klasörünü oluştur.
WORKDIR /app

# Önce sadece package.json dosyasını kopyala. Bu, Docker'ın önbellekleme
# mekanizmasını verimli kullanır ve bağımlılıklar değişmedikçe
# `npm install` komutunu tekrar çalıştırmaz.
COPY package*.json ./

# package.json'da listelenen bağımlılıkları yükle.
RUN npm install

# Projedeki geri kalan tüm dosyaları (.js, .html vb.) kopyala.
COPY . .

# Bu container'ın 8080 portunu dış dünyaya açacağını bildir.
EXPOSE 8080

# Container çalıştığında çalıştırılacak olan varsayılan komut.
CMD ["node", "server.js"]