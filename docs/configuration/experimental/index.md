# Experimental

!!! quote "Changes in sing-box 1.8.0"

    :material-plus: [cache_file](#cache_file)  
    :material-alert-decagram: [clash_api](#clash_api)

### Structure

```json
{
  "experimental": {
    "cache_file": {},
    "clash_api": {},
    "v2ray_api": {},
    "timeout": {},
    "constant": {},
    "urltest_unified_delay": false
  }
}
```

### Fields

| Key          | Format                     |
|--------------|----------------------------|
| `cache_file` | [Cache File](./cache-file/) |
| `clash_api`  | [Clash API](./clash-api/)   |
| `v2ray_api`  | [V2Ray API](./v2ray-api/)   |
| `timeout`    | [Timeout](./timeout/)       |
| `constant`   | [Constant](./constant/)     |

#### urltest_unified_delay

When unified delay is enabled, two delay tests are conducted to eliminate
latency differences caused by connection handshakes and other variations
in different types of nodes.