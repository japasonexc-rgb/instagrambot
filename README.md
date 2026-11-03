# Instagram Marka Hesap Yönetim Paneli

Telegram panelinin (`../`) mimarisiyle yazılmış, markanızın Instagram
hesaplarını tek yerden yönetmek için web paneli.

## Hesap ekleme

**+ Instagram Hesabı Ekle** → kullanıcı adı, şifre ve isterseniz **özel IP**
→ **Giriş Yap**. Hesap panele kaydolur.

- Instagram doğrulama kodu isterse (iki adımlı doğrulama SMS/Authenticator
  kodu ya da "şüpheli giriş" için SMS/e-posta kodu) aynı pencerede kod kutusu
  açılır; kodu girip **Doğrula**'ya basarsınız.
- Aynı hesabı tekrar eklerseniz yeni kayıt açılmaz, mevcut hesabın girişi
  yenilenir. IP alanı boşsa hesap kendi kayıtlı IP'sini korur.
- Şifre ve oturum şifreli saklanır. Instagram oturumu sonlandırırsa panel
  kayıtlı şifreyle **otomatik olarak yeniden giriş** yapar; kod isterse
  hesabın sayfasında **Yeniden Giriş Yap** ile girersiniz.

## Hesaba özel IP

Sunucunun tek bir IP'si vardır. Her hesabın Instagram'a farklı bir IP'den
görünmesi için proxy sağlayıcısından aldığınız IP bilgisini o hesaba
girersiniz:

- `185.12.34.56:8000:kullanıcı:parola` (sağlayıcıların verdiği biçim)
- `socks5://kullanıcı:parola@185.12.34.56:1080`, `http://185.12.34.56:8000`

O hesabın **tüm** Instagram istekleri (giriş dahil) yalnızca kendi IP'sinden
gider. IP çalışmazsa istek sunucu IP'sine düşmez, hata verir; böylece
hesaplar birbirine bağlanmaz. **IP'yi Test Et**, Instagram'ın o hesabı hangi
IP'den gördüğünü gösterir. IP sonradan değiştirilebilir veya kaldırılabilir.

## Toplu paylaşım

Soldaki **Toplu Paylaşım** (ya da bir hesabın sayfasındaki **Gönderi
Paylaş**):

1. **Gönderiyi seçin:** bilgisayardan görsel (JPG/PNG/WebP) veya video
   (MP4/MOV → Reels) seçin ya da sürükleyin, açıklamayı yazın.
2. **Hesapları seçin:** **Tümünü seç** ya da tek tek tikleyin.
3. **Paylaş:** gönderi seçilen hesaplarda listedeki sırayla, aralarında
   belirlediğiniz süre kadar bekleyerek paylaşılır. Her hesap kendi IP'sini
   kullanır.

Her hesabın durumu canlı görünür (sırada / paylaşılıyor / paylaşıldı +
gönderi linki / başarısız + nedeni). Bir hesap hata verirse diğerleri devam
eder; iş bitince **Paylaşılamayan hesaplarda tekrar dene** ile aynı gönderi
yalnızca o hesaplara tekrar gönderilir. Yüklenen dosyalar bunun için 7 gün
saklanır (`data/uploads`). Görseller otomatik olarak düz (EXIF yönü
düzeltilmiş) JPEG'e çevrilir.

## Diğer özellikler

- Profil bilgileri (takipçi, takip, gönderi sayısı)
- Gönderilerin yorumları: arama, toplu silme, yanıtlama, CSV dışa aktarma
- İş kuyruğu: worker sayısı, gecikme, duraklat/devam/durdur, canlı log

**Bilinçli olarak yapılmayan:** Otomatik takip/beğeni/DM ya da başka
hesapların takipçilerini toplama yok. Panel yalnızca markanın kendi
hesaplarını yönetmek için.

## Önemli not

Kullanıcı adı/şifre ile giriş, Instagram'ın resmi API'si değildir; telefon
uygulamasının yaptığı istekleri taklit eder
([instagrapi](https://github.com/subzeroid/instagrapi) kütüphanesi ile).
Instagram bu tür girişleri zaman zaman "şüpheli giriş" olarak işaretleyip kod
ister ya da hesabı geçici kısıtlayabilir. Riski azaltmak için:

- Her hesaba sabit, kaliteli (tercihen mobil veya residential) bir IP atayın
  ve sık değiştirmeyin.
- İlk girişte telefondaki Instagram uygulamasında "Bu bendim" onayı
  istenirse onaylayın.
- Toplu işlemlerde worker sayısını düşük, gecikmeyi yüksek tutun.

## Kurulum ve çalıştırma

Gereksinimler: Go 1.25+, Python 3.9+.

```bash
cd instagram
python3 -m venv bridge/.venv
bridge/.venv/bin/pip install -r bridge/requirements.txt
cp .env.example .env    # JWT_SECRET ve ENCRYPTION_KEY için: openssl rand -hex 32
go run ./cmd/server
```

Panel `http://localhost:8081` adresinde açılır; ilk açılışta yönetici
kullanıcı oluşturulur. Instagram köprüsünü (`bridge/bridge.py`) panel kendisi
başlatır ve kapatır, ayrıca çalıştırmanız gerekmez.

Reels paylaşmak için köprüye ek paket gerekir:
`bridge/.venv/bin/pip install moviepy==1.0.3`

Docker (köprü imajın içinde hazır gelir):

```bash
export JWT_SECRET=$(openssl rand -hex 32)
export ENCRYPTION_KEY=$(openssl rand -hex 32)
docker compose up --build
```

## Mimari

```
cmd/server          → giriş noktası; Instagram köprüsünü başlatır
internal/config     → .env: port, DB, JWT_SECRET, ENCRYPTION_KEY
internal/db         → GORM (SQLite / PostgreSQL) + migration
internal/models     → AdminUser, InstagramAccount, Job, JobLog
internal/crypto     → AES-256-GCM (şifre, oturum ve IP bilgileri şifreli)
internal/auth       → panel girişi (bcrypt + JWT)
internal/instagram  → giriş/kod akışı, otomatik yeniden giriş, hesap başına IP, köprü istemcisi
internal/queue      → arka plan işleri (sıralı toplu paylaşım, yorum silme, CSV)
internal/realtime   → WebSocket hub
internal/api        → REST API (chi)
bridge/bridge.py    → yerel Instagram köprüsü (Python + instagrapi), yalnızca 127.0.0.1
web/                → derleme gerektirmeyen vanilla JS panel
```

## REST API özeti

Tüm `/api/*` uçları (setup/login/ws hariç) `Authorization: Bearer <token>` ister.

| Yöntem & Yol | Açıklama |
|---|---|
| `GET /api/accounts/` | Hesapları listeler |
| `POST /api/accounts/login` | Hesap ekler: `username`, `password`, `proxy_url`, `label` |
| `POST /api/accounts/login/code` | Instagram'ın istediği kodu gönderir: `login_id`, `code` |
| `POST /api/accounts/{id}/relogin` | Yeniden giriş (`password` boşsa kayıtlı şifre) |
| `DELETE /api/accounts/{id}` | Hesabı ve iş geçmişini siler |
| `POST /api/accounts/{id}/sync` | Profili günceller |
| `PUT /api/accounts/{id}/proxy` | IP atar/değiştirir (`proxy_url`, boş = kaldır) |
| `POST /api/accounts/{id}/proxy/test` | Instagram'ın gördüğü IP'yi döner |
| `GET /api/accounts/{id}/media` | Son gönderiler |
| `GET /api/accounts/{id}/media/{mediaID}/comments` | Gönderi yorumları |
| `POST /api/accounts/{id}/media/{mediaID}/comments/{commentID}/reply` | Yoruma yanıt |
| `POST /api/accounts/{id}/jobs/moderate-comments` | Toplu yorum silme işi |
| `POST /api/uploads` | Paylaşılacak dosyayı yükler (multipart `file`), `upload_id` döner |
| `POST /api/bulk-publish` | Toplu paylaşım işi: `upload_id`, `caption`, `account_ids` (sırayla), `delay_sec` |
| `GET /api/bulk-publish/jobs` | Toplu paylaşım işleri |
| `POST /api/accounts/{id}/jobs/publish` | Tek hesapta URL'den paylaşım (`media_type`, `url`, `caption`) |
| `POST /api/accounts/{id}/jobs/export-comments` | Yorum CSV işi |
| `GET /api/accounts/{id}/jobs` | İşler |
| `GET /api/jobs/{id}` · `/logs` · `/download` | İş durumu / loglar / CSV |
| `POST /api/jobs/{id}/pause\|resume\|cancel` | İş kontrolü |

## Güvenlik notları

- Instagram şifreleri, oturumlar ve IP/proxy bilgileri (içindeki parola dahil)
  AES-256-GCM ile şifreli saklanır; ekranda proxy parolası `***` görünür.
- Şifreler loglara yazılmaz. Köprü yalnızca 127.0.0.1'i dinler ve panelin her
  açılışta ürettiği rastgele anahtarı ister.
- CSV dışa aktarmada formül enjeksiyonuna karşı hücreler etkisizleştirilir.
