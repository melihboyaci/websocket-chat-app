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
  } else {
    // Başka bir sayfa istenirse 404 Hatası döndür
    res.writeHead(404);
    res.end();
  }
});

// WebSocket sunucusunu, yukarıda oluşturduğumuz HTTP sunucusuna entegre et
const wss = new WebSocket.Server({ server });

const clients = new Map(); // Changed from Set to Map for better client management

// Yeni bir istemci bağlandığında çalışacak fonksiyon
wss.on('connection', (ws) => {
  const clientId = Date.now() + Math.random(); // Unique client ID
  clients.set(clientId, { ws, username: null });
  console.log(`Yeni bir kullanıcı bağlandı. ID: ${clientId}`);

  // Bağlı bir istemciden mesaj geldiğinde çalışacak fonksiyon
  ws.on('message', (message) => {
    try {
      const stringMessage = message.toString();
      console.log(`Gelen mesaj: ${stringMessage}`);

      // JSON formatında mesaj parse et
      const messageData = JSON.parse(stringMessage);
      
      // Mesajı doğrula ve zenginleştir
      if (messageData.username && messageData.message) {
        // Update client username
        const client = clients.get(clientId);
        if (client) {
          client.username = messageData.username;
        }

        const enrichedMessage = {
          username: messageData.username,
          message: messageData.message,
          timestamp: new Date(),
          channel: messageData.channel || 'genel'
        };

        // Zenginleştirilmiş mesajı tüm aktif istemcilere gönder
        const messageToSend = JSON.stringify(enrichedMessage);
        broadcastMessage(messageToSend);
      }
    } catch (error) {
      console.error('Mesaj işleme hatası:', error);
      // JSON parse hatası durumunda eski format ile devam et
      broadcastMessage(message.toString());
    }
  });

  // Bir istemcinin bağlantısı koptuğunda çalışacak fonksiyon
  ws.on('close', () => {
    const client = clients.get(clientId);
    if (client && client.username) {
      console.log(`Kullanıcı ayrıldı: ${client.username}`);
    }
    clients.delete(clientId);
    console.log(`Bağlantı kapatıldı. ID: ${clientId}`);
  });

  // Hata durumunda
  ws.on('error', (error) => {
    console.error(`WebSocket hatası (ID: ${clientId}):`, error);
    clients.delete(clientId);
  });
});

// Tüm aktif istemcilere mesaj gönderme fonksiyonu
function broadcastMessage(message) {
  const deadClients = [];
  
  for (const [clientId, client] of clients) {
    try {
      if (client.ws.readyState === WebSocket.OPEN) {
        client.ws.send(message);
      } else {
        deadClients.push(clientId);
      }
    } catch (error) {
      console.error(`İstemciye mesaj gönderme hatası (ID: ${clientId}):`, error);
      deadClients.push(clientId);
    }
  }
  
  // Ölü bağlantıları temizle
  deadClients.forEach(clientId => {
    clients.delete(clientId);
    console.log(`Ölü bağlantı temizlendi. ID: ${clientId}`);
  });
}

// HTTP ve WebSocket sunucusunu 8080 portunda dinlemeye başla
const PORT = 8080;
const HOST = '0.0.0.0'; // Tüm IP adreslerinden erişim için
server.listen(PORT, HOST, () => {
  console.log(`Sohbet sunucusu http://localhost:${PORT} adresinde başlatıldı...`);
  console.log(`Dış erişim için: http://[SUNUCU_IP]:${PORT} adresini kullanın`);
});