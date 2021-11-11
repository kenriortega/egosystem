package proxy

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/kenriortega/ngonx/pkg/errors"
	"github.com/kenriortega/ngonx/pkg/logger"
	"github.com/kenriortega/ngonx/pkg/metric"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/gbrlsnchs/jwt/v3"
	domain "github.com/kenriortega/ngonx/internal/proxy/domain"
	services "github.com/kenriortega/ngonx/internal/proxy/services"
)

// proxy global var for management of reverse proxy
var proxy *httputil.ReverseProxy

// JWTPayload custom struc for jwt Payload
type JWTPayload struct {
	jwt.Payload
}

// ResponseMiddleware struct for middleware responses
type ResponseMiddleware struct {
	Message string `json:"message"`
	Code    int    `json:"code"`
}

// ProxyHandler handler for proxy funcionalities
type ProxyHandler struct {
	Service services.DefaultProxyService
}

// SaveSecretKEY handler for save secrets
func (ph *ProxyHandler) SaveSecretKEY(engine, key, apikey string) {
	result, err := ph.Service.SaveSecretKEY(engine, key, apikey)
	if err != nil {
		logger.LogError(errors.Errorf("proxy: SaveSecretKEY %v", err).Error())
	}
	logger.LogInfo("proxy: SaveSecretKEY" + result)
}

// ProxyGateway handler for management all request
func (ph *ProxyHandler) ProxyGateway(endpoints domain.ProxyEndpoint, engine, key, securityType string) {
	for _, endpoint := range endpoints.Endpoints {

		target, err := url.Parse(
			fmt.Sprintf("%s%s", endpoints.HostURI, endpoint.PathEndpoint),
		)
		if err != nil {
			logger.LogError(errors.Errorf("proxy: %v", err).Error())

		}
		if endpoint.PathProtected {
			proxy = httputil.NewSingleHostReverseProxy(target)

			originalDirector := proxy.Director
			proxy.Director = func(req *http.Request) {
				originalDirector(req)
				metricRegister(req, target)

				switch securityType {
				case "jwt":
					err := checkJWTSecretKeyFromRequest(req, key)
					proxy.ModifyResponse = modifyResponse(err)
				case "apikey":
					err := checkAPIKEYSecretKeyFromRequest(req, ph, engine, key)
					proxy.ModifyResponse = modifyResponse(err)
				}

			}
			proxy.ErrorHandler = func(rw http.ResponseWriter, r *http.Request, err error) {
				rw.WriteHeader(http.StatusInternalServerError)
				_, _ = rw.Write([]byte(err.Error()))
			}
			http.Handle(
				endpoint.PathToProxy,
				http.StripPrefix(
					endpoint.PathToProxy,
					proxy,
				),
			)
		} else {

			proxy = httputil.NewSingleHostReverseProxy(target)

			originalDirector := proxy.Director
			proxy.Director = func(req *http.Request) {
				originalDirector(req)
				metricRegister(req, target)

			}
			http.Handle(
				endpoint.PathToProxy,
				http.StripPrefix(
					endpoint.PathToProxy,
					proxy,
				),
			)
		}
	}
}

func metricRegister(req *http.Request, target *url.URL) {
	metric.CountersByEndpoint.With(
		prometheus.Labels{
			"proxyPath":    req.RequestURI,
			"endpointPath": target.String(),
			"ipAddr":       extractIpAddr(req),
			"method":       req.Method,
		},
	).Inc()
	metric.TotalRequests.With(
		prometheus.Labels{
			"path":    req.RequestURI,
			"service": "proxy",
		},
	).Inc()

}

// checkJWTSecretKeyFromRequest check jwt for request
func checkJWTSecretKeyFromRequest(req *http.Request, key string) error {
	header := req.Header.Get("Authorization") // pass to constanst
	hs := jwt.NewHS256([]byte(key))
	now := time.Now()
	if !strings.HasPrefix(header, "Bearer ") {
		logger.LogError(errors.Errorf("proxy: %v", errors.ErrBearerTokenFormat).Error())

		return errors.ErrBearerTokenFormat
	}

	token := strings.Split(header, " ")[1]
	pl := JWTPayload{}
	expValidator := jwt.ExpirationTimeValidator(now)
	validatePayload := jwt.ValidatePayload(&pl.Payload, expValidator)

	_, err := jwt.Verify([]byte(token), hs, &pl, validatePayload)

	if errors.ErrorIs(err, jwt.ErrExpValidation) {
		logger.LogError(errors.Errorf("proxy: %v", errors.ErrTokenExpValidation).Error())

		return errors.ErrTokenExpValidation
	}
	if errors.ErrorIs(err, jwt.ErrHMACVerification) {
		logger.LogError(errors.Errorf("proxy: %v", errors.ErrTokenHMACValidation).Error())

		return errors.ErrTokenHMACValidation
	}

	return nil
}

// checkAPIKEYSecretKeyFromRequest check apikey from request
func checkAPIKEYSecretKeyFromRequest(req *http.Request, ph *ProxyHandler, engine, key string) error {
	apikey, err := ph.Service.GetKEY(engine, key)
	header := req.Header.Get("X-API-KEY") // pass to constants
	if err != nil {
		logger.LogError(errors.Errorf("proxy: %v", errors.ErrGetkeyView).Error())

	}
	if apikey == header {
		logger.LogInfo("proxy: check secret from request OK")
		return nil
	} else {
		logger.LogError(errors.Errorf("proxy: Invalid API KEY").Error())
		return errors.NewError("Invalid API KEY")
	}
}

// modifyResponse modify response
func modifyResponse(err error) func(*http.Response) error {
	return func(resp *http.Response) error {
		resp.Header.Set("X-Proxy", "Ngonx")

		if err != nil {
			return err
		}
		return nil
	}
}

func extractIpAddr(req *http.Request) string {
	ipAddress := req.RemoteAddr
	fwdAddress := req.Header.Get("X-Forwarded-For") // capitalisation doesn't matter
	if fwdAddress != "" {
		// Got X-Forwarded-For
		ipAddress = fwdAddress // If it's a single IP, then awesome!

		// If we got an array... grab the first IP
		ips := strings.Split(fwdAddress, ", ")
		if len(ips) > 1 {
			ipAddress = ips[0]
		}
	}
	remoteAddrToParse := ""
	if strings.Contains(ipAddress, "[::1]") {
		remoteAddrToParse = strings.Replace(ipAddress, "[::1]", "localhost", -1)
		ipAddress = strings.Split(remoteAddrToParse, ":")[0]
	} else {
		ipAddress = strings.Split(ipAddress, ":")[0]
	}
	return ipAddress
}
