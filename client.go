package pht

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	Host       string
	Key        string
	httpClient *http.Client
	manager    *sessionManager
}

func NewClient(host, key string) *Client {
	return &Client{
		Host:       host,
		Key:        key,
		httpClient: &http.Client{},
		manager:    newSessionManager(),
	}
}

func (c *Client) Dial() (net.Conn, error) {
	r, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://%s%s", c.Host, tokenURI), nil)
	if err != nil {
		return nil, err
	}
	r.Header.Set("Authorization", "key="+c.Key)
	resp, err := c.httpClient.Do(r)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, errors.New(resp.Status)
	}

	data, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	token := strings.TrimPrefix(string(data), "token=")
	if token == "" {
		return nil, errors.New("invalid token")
	}

	session := newSession(0, 0)
	c.manager.SetSession(token, session)

	go c.sendDataLoop(token)
	go c.recvDataLoop(token)

	return newConn(session), nil
}

func (c *Client) sendDataLoop(token string) error {
	session := c.manager.GetSession(token)
	if session == nil {
		return errors.New("invalid token")
	}

	for {
		select {
		case <-session.closed:
			c.manager.DelSession(token)
			return errors.New("session closed")

		case b := <-session.wchan:
			r, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://%s%s", c.Host, pushURI), bytes.NewReader(b))
			if err != nil {
				return err
			}
			r.Header.Set("Authorization", fmt.Sprintf("key=%s; token=%s", c.Key, token))
			resp, err := c.httpClient.Do(r)
			if err != nil { // TODO: retry
				return err
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return errors.New(resp.Status)
			}
		}
	}
}

func (c *Client) recvDataLoop(token string) error {
	session := c.manager.GetSession(token)
	if session == nil {
		return errors.New("invalid token")
	}

	for {
		r, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://%s%s", c.Host, pollURI), nil)
		if err != nil {
			return err
		}
		r.Header.Set("Authorization", fmt.Sprintf("key=%s; token=%s", c.Key, token))
		resp, err := c.httpClient.Do(r)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return errors.New(resp.Status)
		}

		for {
			select {
			case <-session.closed:
				c.manager.DelSession(token)
				return errors.New("session closed")
			default:
			}

			buf := make([]byte, 16*1024)
			nr, er := resp.Body.Read(buf)
			if nr > 0 {
				select {
				case session.rchan <- buf[:nr]:
				case <-session.closed:
					c.manager.DelSession(token)
					return errors.New("session closed")
				case <-time.After(time.Second * 90):
					c.manager.DelSession(token)
					return errors.New("timeout")
				}
			}
			if er == io.EOF {
				break
			}
			if er != nil {
				err = er
				break
			}
		}
	}
}
