# cisid — Kod Kuralları

Bu dosya, projede kod yazarken (insan ya da agent) uyulması zorunlu kuralları içerir.
- Her kod değişikliğinden veya yeni dosya oluşturduktan sonra otomatik olarak `go fmt ./...` ve `go vet ./...` komutlarını çalıştır.
- Eğer `go build` veya `go test` sırasında import hatası veya derleme hatası alırsan, durup bana sorma; hatayı kendin analiz et ve düzelt.


## Git ve Commit Kuralları (Zorunlu)
- Her mantıksal ve önemli adımda (örneğin; bir modülün kodu ve testleri tamamlandığında, yeni bir özellik eklendiğinde) **otomatik olarak Git commit'i oluştur**.
- Commit atmadan ÖNCE mutlaka `go build ./...` ve `go test ./...` komutlarını çalıştır. **Asla derlenmeyen (compile error) veya testi başarısız (failing test) kodu commit etme.** Hata varsa önce onu düzelt.
- **Conventional Commits** formatını kullanma. (Örn: `feat(api): add github webhook handler`, `test(runner): add docker mock tests`, `fix(agent): handle websocket reconnect`).
- Commit mesajları kısa, net ve yapılan değişikliğin "ne" ve "neden" yapıldığını açıklamalıdır.
- Eğer yeni bir dosya eklendiyse, önce `git add .` yap, sonra commit'le.
- contributor 'sidumandr' olmalı.

## 1. Docker SDK

- Güncel resmi Go SDK'sını kullan:
  - `github.com/moby/moby/client` (client, v0.6.x)
  - `github.com/moby/moby/api` (tipler, v1.56.x)
- `github.com/docker/docker/client` **kullanma**. Bu modül v28.5.2'de (Kasım 2025) dondu ve artık güncellenmiyor.
- Eski örneklerdeki API'leri kopyalama. Client'ı şöyle oluştur:

```go
cli, err := client.New(client.FromEnv)
if err != nil {
	return fmt.Errorf("create docker client: %w", err)
}
defer cli.Close()
```

## 2. Context

- I/O yapan, bloklayan ya da iptal edilebilen her fonksiyonun **ilk parametresi** `ctx context.Context` olmalı.
- Context'i struct alanında saklama; parametre olarak taşı.
- Kütüphane kodunda `context.Background()` / `context.TODO()` kullanma. Sadece `main` ve testlerde kullanılabilir.
- Context'li API'leri tercih et: `QueryContext`, `ExecContext`, `http.NewRequestWithContext` vb.
- Goroutine başlatırken onu durduracak bir `ctx` mutlaka geçir.

```go
func (r *Runner) RunStep(ctx context.Context, step models.Step) error
```

## 3. Hata Sarmalama (Go 1.13+)

- Alt katmandan gelen hatayı her zaman `%w` ile sarmala ve ne yapılırken oluştuğunu yaz:

```go
if err != nil {
	return fmt.Errorf("insert job %d: %w", job.ID, err)
}
```

- Mevcut bir hatayı `errors.New` ile yeniden oluşturma ve `%v` ile sarmalama; zincir kopar.
- `errors.New` sadece paket seviyesindeki sentinel hatalar için kullanılır:

```go
var ErrJobNotFound = errors.New("job not found")
```

- Hata kontrolünde `==` yerine `errors.Is` / `errors.As` kullan.

## 4. Race Condition

- WebSocket, goroutine ve paylaşılan state içeren her kod race detector ile test edilmeli:

```bash
go test -race ./...
```

```bash
go run -race ./cmd/server
```

- Paylaşılan state'i `sync.Mutex` / `sync.RWMutex` ile koru ya da sahipliği tek goroutine'e ver ve channel ile haberleş.
- Bir WebSocket bağlantısına aynı anda sadece **bir** goroutine yazabilir. Yazmaları tek bir writer goroutine'e topla.
- Başlattığın her goroutine'in nasıl sonlanacağı belli olmalı (`ctx.Done()`, kapanan channel, `sync.WaitGroup` / `errgroup`).

## 5. SQL Güvenliği

- **Sadece parametreli sorgu / prepared statement kullan.** SQL'i asla string birleştirme (`+`, `fmt.Sprintf`) ile kurma.
- PostgreSQL placeholder'ları: `$1`, `$2`, ...

```go
// Doğru
row := db.QueryRowContext(ctx, "SELECT id, status FROM jobs WHERE id = $1", id)

// YANLIŞ — asla yapma
row := db.QueryRowContext(ctx, "SELECT id, status FROM jobs WHERE id = " + id)
```

- Dinamik tablo/kolon adı veya `ORDER BY` gerekiyorsa değeri kullanıcıdan alma; sabit bir izin listesinden (allowlist) seç.
