# ADR 001: Agent ↔ server protokolü WebSocket'te kalıyor

- **Durum:** Kabul edildi
- **Tarih:** 2026-11-03
- **İlgili:** [ROADMAP.md](../../ROADMAP.md) Faz 4, [agent.md](../agent.md), [hub.md](../hub.md)

## Bağlam

ROADMAP'te Faz 4 için "Agent ↔ server iletişimini gRPC'ye taşı (`api/proto`)" maddesi vardı. Proje iskeleti bu yüzden `api/proto` dizinini ayırdı.

Bugünkü protokol tek bir WebSocket bağlantısı üzerinde JSON zarflarıdır (`{id, type, payload}`, tipler `pkg/models/protocol.go` içinde). Protokolün asıl işi taşıma katmanında değil, uygulama katmanında yapılıyor:

- **Onaylı teslim:** Agent log ve sonuçları bir outbox'ta tutar. Server toplu `ack` gönderir, bağlantı koparsa onaylanmamış mesajlar yeniden gönderilir, server tekrarları ayıklar.
- **Sahiplik:** Job'lar lease ile bir agent'a bağlıdır. Agent yeniden bağlandığında `agent_hello` içindeki `running_job_id` ile job'unu geri alır (`AdoptJob`).
- **Drain:** Server kapanırken `server_closing` gönderir. Agent bekleyen mesajlarını boşaltır ve başka bir server'a bağlanır.
- **İptal:** `job_cancel` server'dan agent'a gider.

Bunların hepsi ağ kesintisi, server yeniden başlatma ve agent'ın kaybolduğu durumlar için test edildi (TCP proxy ile bağlantı kesme testleri, mutasyon kontrolü).

## Karar

Agent ↔ server protokolü şimdilik WebSocket + JSON olarak kalıyor. gRPC'ye geçilmiyor. `api/proto` dizini ileride ihtiyaç olursa diye ayrılmış durumda kalıyor.

## Gerekçe

1. **Teslim garantileri taşımadan bağımsız.** gRPC çift yönlü stream'i de bağlantı koptuğunda mesaj kaybeder. Ack, yeniden gönderme, tekilleştirme ve lease mantığı gRPC'de de aynen yazılmak zorunda. Geçiş bu kodu sadeleştirmez, yalnızca başka bir taşımaya taşır.
2. **Ağ uyumluluğu.** WebSocket bir HTTP/1.1 upgrade'idir. Kurumsal HTTP proxy'lerinden, HTTP/1.1 konuşan L7 load balancer'lardan ve çoğu reverse proxy'den ek ayar olmadan geçer. gRPC uçtan uca HTTP/2 ister, bazı proxy ve load balancer'larda ayrı yapılandırma gerektirir.
3. **Tek port, tek sertifika.** Agent endpoint'i (`/agent/ws`) webhook, API ve web arayüzüyle aynı `net/http` server'ında, aynı portta ve aynı TLS sertifikasıyla çalışır. gRPC ya ayrı bir port ya da HTTP/1.1 ile HTTP/2'yi aynı portta ayıran ek bir katman ister.
4. **Bağımlılık ve build.** gRPC, `google.golang.org/grpc`, protobuf ve kod üretimi (`protoc` ya da `buf`) getirir. Şu an build yalnızca `go build` ile yapılıyor.
5. **Mesajlar küçük ve seyrek.** Trafiğin çoğu log parçaları ve onaylardır. JSON serileştirmenin şu anki ölçekte darboğaz olduğuna dair bir işaret yok. Bu ölçülmedi, yalnızca bir varsayım.

## Sonuçlar

- Protokolün sözleşmesi Go tipleri (`pkg/models/protocol.go`) ve [agent.md](../agent.md#protokol) belgesidir. Şema dosyası ve otomatik istemci üretimi yoktur.
- Go dışında bir dilde agent yazmak isteyen, protokolü belgeden uygulamak zorundadır.
- Mesaj tiplerinde yapılan değişiklikler geriye uyumlu tutulmalıdır: yeni alanlar isteğe bağlı eklenir, bilinmeyen alanlar yok sayılır.

## Ne zaman yeniden değerlendirilir

- Go dışında agent'lar gerekirse ve şemadan istemci üretmek değer kazanırsa.
- Ölçüm, JSON serileştirmenin ya da WebSocket çerçevelemenin darboğaz olduğunu gösterirse.
- Artifact yükleme gibi büyük, akışlı veri transferleri eklenirse. Bu durumda ayrı bir HTTP endpoint'i de bir seçenek.
