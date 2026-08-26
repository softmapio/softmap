module example.com/toyshop

go 1.22

require (
	github.com/gin-gonic/gin v1.0.0
	github.com/go-chi/chi/v5 v5.0.0
	github.com/go-ozzo/ozzo-validation/v4 v4.0.0
	github.com/golang-jwt/jwt/v5 v5.0.0
	github.com/jackc/pgx/v5 v5.0.0
	github.com/labstack/echo/v4 v4.0.0
	github.com/minio/minio-go/v7 v7.0.0
	github.com/redis/go-redis/v9 v9.0.0
	github.com/segmentio/kafka-go v1.0.0
	golang.org/x/crypto v0.1.0
	google.golang.org/grpc v1.0.0
)

// All dependencies are local stubs so tests are hermetic and fast; the stubs
// carry only the signatures softmap matches on.
replace (
	github.com/gin-gonic/gin => ../stubs/gin
	github.com/go-chi/chi/v5 => ../stubs/chi
	github.com/go-ozzo/ozzo-validation/v4 => ../stubs/ozzo
	github.com/golang-jwt/jwt/v5 => ../stubs/jwt
	github.com/jackc/pgx/v5 => ../stubs/pgx
	github.com/labstack/echo/v4 => ../stubs/echo
	github.com/minio/minio-go/v7 => ../stubs/minio
	github.com/redis/go-redis/v9 => ../stubs/redis
	github.com/segmentio/kafka-go => ../stubs/kafkago
	golang.org/x/crypto => ../stubs/xcrypto
	google.golang.org/grpc => ../stubs/grpc
)
