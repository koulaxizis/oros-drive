# A real third-party WebDAV server (WsgiDAV) for CI, so the WebDAV
# client is also checked against code we did not write:
#
#   python interop/davserver.py <folder> <port> stock|ns
#
# stock: WsgiDAV as shipped. Its ETags change once a second, so the
#        connection check must refuse it (dav-conditions).
# ns:    ETags from the nanosecond time, like Nextcloud's: the check
#        must pass and every store promise must hold.
# User "chris", password "pw".
import hashlib
import os
import sys

from cheroot import wsgi
from wsgidav import util
from wsgidav.wsgidav_app import WsgiDAVApp

root, port, mode = sys.argv[1], int(sys.argv[2]), sys.argv[3]
if mode == "ns":
    def etag(p):
        st = os.stat(p)
        return "%s-%s-%s" % (hashlib.md5(p.encode()).hexdigest(), st.st_mtime_ns, st.st_size)
    util.get_file_etag = etag
os.makedirs(root, exist_ok=True)
app = WsgiDAVApp({
    "host": "127.0.0.1", "port": port, "provider_mapping": {"/": root},
    "http_authenticator": {"domain_controller": None, "accept_basic": True,
                           "accept_digest": False, "default_to_digest": False},
    "simple_dc": {"user_mapping": {"*": {"chris": {"password": "pw"}}}},
    "verbose": 1, "logging": {"enable_loggers": []},
})
wsgi.Server(("127.0.0.1", port), app).start()
