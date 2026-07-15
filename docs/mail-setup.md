# Mail Stack Setup (Postfix + Dovecot virtual mailboxes)

CypherPanel provisions **virtual mailboxes** (mail-stack skill): the panel/agent
manage a MariaDB auth database (`virtual_domains`, `virtual_users`) that Postfix
and Dovecot query directly. Mailboxes are decoupled from Linux logins. This
document is the reference MTA configuration that reads that database — the agent
creates the schema and rows (`internal/mailstore`); the operator points Postfix
and Dovecot at it once.

Enable mailbox provisioning by giving the agent a DSN to the mail auth DB:

```sh
CYPHER_AGENT_MAIL_DSN="cypher_mail:PASS@tcp(127.0.0.1:3306)/mailbox"
```

## Auth-DB schema (created by the agent)

```sql
CREATE TABLE virtual_domains (id INT AUTO_INCREMENT PRIMARY KEY, name VARCHAR(255) UNIQUE);
CREATE TABLE virtual_users (
  id INT AUTO_INCREMENT PRIMARY KEY, domain_id INT NOT NULL,
  email VARCHAR(255) UNIQUE, password VARCHAR(255), maildir VARCHAR(255),
  quota BIGINT DEFAULT 0,
  FOREIGN KEY (domain_id) REFERENCES virtual_domains(id) ON DELETE CASCADE);
```

Passwords are **bcrypt** hashes (`$2a$…`), computed in CypherCore — plaintext
never reaches the agent or the database.

## Postfix (SMTP) — MySQL maps

`/etc/postfix/mysql-virtual-domains.cf`
```
user = cypher_mail
password = PASS
hosts = 127.0.0.1
dbname = mailbox
query = SELECT 1 FROM virtual_domains WHERE name='%s'
```

`/etc/postfix/mysql-virtual-mailboxes.cf`
```
user = cypher_mail
password = PASS
hosts = 127.0.0.1
dbname = mailbox
query = SELECT maildir FROM virtual_users WHERE email='%s'
```

`main.cf` (excerpt):
```
virtual_mailbox_domains = mysql:/etc/postfix/mysql-virtual-domains.cf
virtual_mailbox_maps    = mysql:/etc/postfix/mysql-virtual-mailboxes.cf
virtual_mailbox_base    = /var/mail/vhosts
virtual_transport       = lmtp:unix:private/dovecot-lmtp
smtpd_sasl_type = dovecot
smtpd_sasl_path = private/auth
```

## Dovecot (IMAP/POP3/LMTP) — SQL auth

`/etc/dovecot/dovecot-sql.conf.ext`
```
driver = mysql
connect = host=127.0.0.1 dbname=mailbox user=cypher_mail password=PASS
default_pass_scheme = BLF-CRYPT   # reads the bcrypt hashes CypherCore stores
password_query = SELECT email AS user, password FROM virtual_users WHERE email='%u'
user_query = SELECT maildir, 5000 AS uid, 5000 AS gid, \
  concat('*:bytes=', quota) AS quota_rule FROM virtual_users WHERE email='%u'
```

Mail is stored in Maildir under `/var/mail/vhosts/<domain>/<user>/` (owned by
the `vmail` uid/gid, e.g. 5000). The agent creates the Maildir on provisioning.

## Deliverability records

On mailbox creation CypherCore publishes **MX / SPF / DMARC** via the DNS layer
automatically (a `mail.<domain>` A record too, when the server IP is known).

- **DKIM** requires a signer (Rspamd or OpenDKIM) with a per-domain key; generate
  the keypair, keep the private key `0600`, publish the public key as
  `default._domainkey.<domain>` TXT, and configure the signer. This signer setup
  is the remaining mail sub-task (tracked in `task.md`).

## Spam filtering

**Rspamd** in front of Postfix (milter) handles spam scoring/greylisting; wire it
via `smtpd_milters = inet:localhost:11332`. Rspamd also hosts DKIM signing.
