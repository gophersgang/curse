package main

import (
	"fmt"
	"log"
	"net/http"
	"time"

	"golang.org/x/crypto/ssh"
)

type httpParams struct {
	bastionIP   string
	bastionUser string
	cmd         string
	key         string
	remoteUser  string
	userIP      string
}

func webHandler(w http.ResponseWriter, r *http.Request, conf *config) {
	// Do basic auth with the reverse proxy to prevent side-stepping it
	user, pass, ok := r.BasicAuth()
	if !ok {
		http.Error(w, "Authorization Failure", http.StatusUnauthorized)
		return
	}
	if user != conf.ProxyUser || pass != conf.ProxyPass {
		log.Printf("Invalid proxy credentials")
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Load our form parameters into a struct
	p := httpParams{
		bastionIP:   r.PostFormValue("bastionIP"),
		bastionUser: r.Header.Get(conf.UserHeader),
		cmd:         r.PostFormValue("cmd"),
		key:         r.PostFormValue("key"),
		remoteUser:  r.PostFormValue("remoteUser"), // FIXME this should be re-evaluated as a daemon config option
		userIP:      r.PostFormValue("userIP"),
	}

	// Set our certificate validity times
	va := time.Now()
	vb := time.Now().Add(conf.dur)

	// Generate a fingerprint of the received public key for our key_id string
	fp := ""
	pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(p.key))
	if err != nil {
		log.Printf("Unable to parse authorized key |%s|", p.key)
		http.Error(w, "Unable to parse authorized key", http.StatusBadRequest)
		return
	}
	fp = ssh.FingerprintLegacyMD5(pk)

	// Generate our key_id for the certificate
	//keyID := fmt.Sprintf("user[%s] from[%s] command[%s] sshKey[%s] ca[%s] valid to[%s]",
	keyID := fmt.Sprintf("user[%s] from[%s] command[%s] sshKey[%s] valid to[%s]",
		p.bastionUser, p.userIP, p.cmd, fp, vb.Format(time.RFC3339))

	// Log the request
	log.Printf("Request: |%s|", keyID)

	// Make sure we have everything we need from our parameters
	err = validateHTTPParams(p, conf)
	if err != nil {
		errMsg := fmt.Sprintf("Param validation failure: %v", err)
		log.Printf(errMsg)
		http.Error(w, errMsg, http.StatusBadRequest)
		return
	}

	// Check if we've seen this pubkey before and if it's too old
	expired, err := checkPubKeyAge(conf, fp)
	if expired {
		http.Error(w, "Submitted pubkey is too old. Please generate new key.", http.StatusUnprocessableEntity)
		return
	}

	// Set all of our certificate options
	cc := certConfig{
		certType:    ssh.UserCert,
		command:     p.cmd,
		extensions:  conf.exts,
		keyID:       keyID,
		principals:  []string{p.remoteUser},
		srcAddr:     p.bastionIP,
		validAfter:  va,
		validBefore: vb,
	}

	// Sign the public key
	authorizedKey, err := signPubKey(conf.caSigner, []byte(p.key), cc)
	if err != nil {
		log.Printf("%v", err)
		http.Error(w, "Server error", http.StatusInternalServerError)
		return
	}

	w.Write(authorizedKey)
}

func validateHTTPParams(p httpParams, conf *config) error {
	if conf.ForceCmd && p.cmd == "" {
		err := fmt.Errorf("cmd missing from request")
		return err
	}
	if p.bastionIP == "" || !validIP(p.bastionIP) {
		err := fmt.Errorf("bastionIP is invalid")
		return err
	}
	if p.bastionUser == "" {
		err := fmt.Errorf("%s missing from request", conf.UserHeader)
		return err
	} else if len(p.bastionUser) > 32 || !conf.userRegex.MatchString(p.bastionUser) {
		err := fmt.Errorf("username is invalid")
		return err
	}
	if p.key == "" {
		err := fmt.Errorf("key missing from request")
		return err
	}
	if p.remoteUser == "" {
		err := fmt.Errorf("remoteUser missing from request")
		return err
	}
	if conf.RequireClientIP && !validIP(p.userIP) {
		err := fmt.Errorf("invalid userIP")
		log.Printf("invalid userIP: |%s|", p.userIP) // FIXME This should be re-evaluated in the logging refactor
		return err
	}

	return nil
}
