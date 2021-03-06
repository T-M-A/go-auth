package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"gopkg.in/mgo.v2/bson"
	"io/ioutil"
	"net/http"

	pb "github.com/micro-company/go-auth/grpc/mail"
	grpcServer "github.com/micro-company/go-auth/grpc/server"
	"github.com/micro-company/go-auth/middleware"
	"github.com/micro-company/go-auth/utils/crypto"

	"github.com/go-chi/chi"
	chiMiddleware "github.com/go-chi/chi/middleware"
	"github.com/micro-company/go-auth/handlers/user"
	"github.com/micro-company/go-auth/models/session"
	"github.com/micro-company/go-auth/models/user"
	"github.com/micro-company/go-auth/utils"
	"github.com/sirupsen/logrus"
)

var log = logrus.New()

func init() {
	// Logging =================================================================
	// Setup the logger backend using Sirupsen/logrus and configure
	// it to use a custom JSONFormatter. See the logrus docs for how to
	// configure the backend at github.com/Sirupsen/logrus
	log.Formatter = new(logrus.JSONFormatter)
}

// Routes creates a REST router
func Routes() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Captcha)
	r.Use(chiMiddleware.AllowContentType("application/json"))

	r.Get("/debug/{token}", Debug)
	r.Post("/", Login)
	r.Post("/new", Registration)
	r.Post("/recovery", Recovery)
	r.Post("/recovery/{token}", RecoveryByToken)
	r.Post("/refresh", Refresh)
	r.Delete("/", Logout)

	return r
}

func Login(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	b, err := ioutil.ReadAll(r.Body)
	defer r.Body.Close()
	if err != nil {
		utils.Error(w, errors.New(`"`+err.Error()+`"`))
		return
	}

	var user *userModel.User
	err = json.Unmarshal(b, &user)
	if err != nil {
		utils.Error(w, errors.New(`"`+err.Error()+`"`))
		return
	}

	var passwordUser = user.Password
	var searchUser = userModel.User{Email: user.Email}
	user, err = userModel.FindOne(searchUser)
	if err != nil {
		utils.Error(w, errors.New(`{"mail":"incorrect mail or password"}`))
		return
	}

	isErr := crypto.CheckPasswordHash(passwordUser, user.Password)
	if !isErr {
		utils.Error(w, errors.New(`{"mail":"incorrect mail or password"}`))
		return
	}

	// Create JWT token
	tokenString, refreshToken, err := CreateJWTToken()
	if err != nil {
		utils.Error(w, errors.New(`"`+err.Error()+`"`))
		return
	}

	w.Header().Set("Authorization", tokenString)
	w.WriteHeader(http.StatusCreated)
	w.Write([]byte(`{
		"tokens": {
			"access": "` + tokenString + `",
			"refresh": "` + refreshToken + `"
		}
	}`))
}

func Registration(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	b, err := ioutil.ReadAll(r.Body)
	defer r.Body.Close()
	if err != nil {
		utils.Error(w, errors.New(`"`+err.Error()+`"`))
		return
	}

	// And now set a new body, which will simulate the same data we read:
	r.Body = ioutil.NopCloser(bytes.NewBuffer(b))

	user.Create(w, r)
}

func Logout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	var Authorization = r.Header.Get("Authorization")
	if Authorization == "" {
		w.WriteHeader(http.StatusUnauthorized)
		utils.Error(w, errors.New(`"not auth"`))
		return
	}

	token, err := sessionModel.VerifyToken(Authorization)
	if err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		utils.Error(w, errors.New(`"`+err.Error()+`"`))
		return
	}

	if !token.Valid {
		w.WriteHeader(http.StatusUnauthorized)
		utils.Error(w, errors.New(`"token invalid"`))
		return
	}

	err = sessionModel.Delete(Authorization)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		utils.Error(w, errors.New(`"`+err.Error()+`"`))
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{}`))
}

func Refresh(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	var TOKEN_REFRESH = r.Header.Get("Authorization")
	if TOKEN_REFRESH == "" {
		w.WriteHeader(http.StatusUnauthorized)
		utils.Error(w, errors.New(`"not auth"`))
		return
	}

	// Chech REFRESH TOKEN
	status, err := sessionModel.CheckRefreshToken(TOKEN_REFRESH)
	if err != nil || status != true {
		w.WriteHeader(http.StatusUnauthorized)
		utils.Error(w, errors.New(`"`+err.Error()+`"`))
		return
	}

	// Create JWT token
	tokenString, refreshToken, err := CreateJWTToken()
	if err != nil {
		utils.Error(w, errors.New(`"`+err.Error()+`"`))
		return
	}

	w.Header().Set("Authorization", tokenString)
	w.WriteHeader(http.StatusCreated)
	w.Write([]byte(`{
		"tokens": {
			"access": "` + tokenString + `",
			"refresh": "` + refreshToken + `"
		}
	}`))
}

func Recovery(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	b, err := ioutil.ReadAll(r.Body)
	defer r.Body.Close()
	if err != nil {
		utils.Error(w, errors.New(`"`+err.Error()+`"`))
		return
	}

	var user *userModel.User
	err = json.Unmarshal(b, &user)
	if err != nil {
		utils.Error(w, errors.New(`"`+err.Error()+`"`))
		return
	}

	// search user by mail
	searchUser := userModel.User{}
	searchUser.Email = user.Email
	user, err = userModel.FindOne(searchUser)
	if err != nil {
		utils.Error(w, errors.New(`{"mail":"incorrect mail"}`))
		return
	}

	// get refresh token
	recoveryLink, err := sessionModel.NewRecoveryLink(user.Id)
	if err != nil {
		utils.Error(w, errors.New(`"`+err.Error()+`"`))
		return
	}

	// Send mail
	conn := grpcServer.GetConnClient()
	c := pb.NewMailClient(conn)
	_, err = c.SendMail(context.Background(), &pb.MailRequest{
		Template: "recovery",
		Mail:     *user.Email,
		Url:      "http://localhost:3000/recovery/" + recoveryLink,
	})
	if err != nil {
		utils.Error(w, errors.New("\"failed to send message\""))
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{}`))
}

func RecoveryByToken(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	b, err := ioutil.ReadAll(r.Body)
	defer r.Body.Close()
	if err != nil {
		utils.Error(w, errors.New(`"`+err.Error()+`"`))
		return
	}

	var user *userModel.User
	err = json.Unmarshal(b, &user)
	if err != nil {
		utils.Error(w, errors.New(`"`+err.Error()+`"`))
		return
	}

	// check correct a password
	if user.Password != user.PasswordRetry {
		utils.Error(w, errors.New(`{"retryPassword":"incorrect new password"}`))
		return
	}

	userId, _ := sessionModel.GetValueByKey(user.RecoveryToken)
	if len(userId) == 0 {
		utils.Error(w, errors.New(`"not found"`))
		return
	}

	t := bson.ObjectIdHex(userId)
	user.Id = t.String()
	user.Password, _ = crypto.HashPassword(user.Password)

	user, err = userModel.Update(user)
	if err != nil {
		utils.Error(w, errors.New(`"`+err.Error()+`"`))
		return
	}
	err = sessionModel.Delete(user.RecoveryToken)
	if err != nil {
		utils.Error(w, errors.New("not found"))
		return
	}
	// TODO: Send mail (theme: New password)

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{}`))
}

func Debug(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	w.WriteHeader(http.StatusBadRequest)
	w.Write([]byte(`{}`))
}
