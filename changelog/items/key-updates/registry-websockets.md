- Added new /skynet/registry/subscribe endpoint which upgrades the http
  connection to a websocket connection and then allows clients to be notified
of changes to registry entries instead of having to poll the portal
periodically.
