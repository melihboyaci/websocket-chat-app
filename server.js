const WebSocket = require('ws');
const http = require('http');
const fs = require('fs');
const path = require('path');

// Gelen isteklere cevap verecek bir HTTP sunucusu oluştur
const server = http.createServer((req, res) => {
  // Eğer tarayıcı ana sayfayı ('/') istiyorsa, index.html dosyasını gönder
  if (req.url === '/' && req.method === 'GET') {
    const filePath = path.join(__dirname, 'index.html');
    fs.readFile(filePath, (err, data) => {
      if (err) {
        res.writeHead(500);
        res.end('Error loading index.html');
        return;
      }
      res.writeHead(200, { 'Content-Type': 'text/html' });
      res.end(data);
    });
  } else if (req.url === '/styles.css' && req.method === 'GET') {
    const filePath = path.join(__dirname, 'styles.css');
    fs.readFile(filePath, (err, data) => {
      if (err) {
        res.writeHead(500);
        res.end('Error loading styles.css');
        return;
      }
      res.writeHead(200, { 'Content-Type': 'text/css' });
      res.end(data);
    });
  } else {
    // Başka bir sayfa istenirse 404 Hatası döndür
    res.writeHead(404);
    res.end();
  }
});

// WebSocket sunucusunu, yukarıda oluşturduğumuz HTTP sunucusuna entegre et
const wss = new WebSocket.Server({ server });

const clients = new Set();

// Yeni bir istemci bağlandığında çalışacak fonksiyon
wss.on('connection', (ws) => {
  clients.add(ws);
  console.log('Yeni bir kullanıcı bağlandı.');

  // Bağlı bir istemciden mesaj geldiğinde çalışacak fonksiyon
  ws.on('message', (message) => {
    const stringMessage = message.toString();
    console.log(`Gelen mesaj: ${stringMessage}`);

    // Try to parse as JSON, fallback to plain text
    let messageData;
    try {
      messageData = JSON.parse(stringMessage);
    } catch (e) {
      // Fallback for plain text messages
      messageData = {
        message: stringMessage,
        username: 'Misafir',
        timestamp: new Date().toISOString()
      };
    }

    // Gelen mesajı bağlı olan TÜM istemcilere gönder (broadcast)
    for (const client of clients) {
      if (client.readyState === WebSocket.OPEN) {
        client.send(JSON.stringify(messageData));
      }
    }
  });

  // Bir istemcinin bağlantısı koptuğunda çalışacak fonksiyon
  ws.on('close', () => {
    clients.delete(ws);
    console.log('Bir kullanıcı ayrıldı.');
  });
});

// HTTP ve WebSocket sunucusunu 8080 portunda dinlemeye başla
const PORT = 8080;
server.listen(PORT, () => {
  console.log(`Sohbet sunucusu http://localhost:${PORT} adresinde başlatıldı...`);
});