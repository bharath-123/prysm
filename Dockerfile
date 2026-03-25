  FROM golang:1.25-alpine AS builder                      
  RUN apk add --no-cache git gcc musl-dev linux-headers
  WORKDIR /app                                            
  COPY go.mod go.sum ./                                   
  RUN go mod download                                     
  COPY . .                                                
  RUN go build -o /beacon-chain ./cmd/beacon-chain        
                                                          
  FROM alpine:3.19
  RUN apk add --no-cache ca-certificates bash             
  COPY --from=builder /beacon-chain
  /usr/local/bin/beacon-chain
  ENTRYPOINT ["beacon-chain"]
