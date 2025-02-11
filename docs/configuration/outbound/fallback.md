### Structure

```json
{
  "type": "fallback",
  "tag": "auto",

  "outbounds": [
    "proxy-a",
    "proxy-b",
    "proxy-c"
  ],
  "providers": [
    "provider-a",
    "provider-b",
    "provider-c",
  ],
  "use_all_providers": false,
  "url": "",
  "interval": "",
  "max_delay": 0,
  "idle_timeout": "",
  "interrupt_exist_connections": false

  ... // Filter Fields
}
```

!!! note ""

    You can ignore the JSON Array [] tag when the content is only one item

### Fields

#### outbounds

List of outbound tags to test.

#### providers

List of providers tags to select.

#### use_all_providers

Use all providers to fill `outbounds`.

#### url

The URL to test. `https://www.gstatic.com/generate_204` will be used if empty.

#### interval

The test interval. `3m` will be used if empty.

#### max_delay

The maximum delay should it be qualify to be picked.

#### idle_timeout

The idle timeout. `30m` will be used if empty.

#### interrupt_exist_connections

Interrupt existing connections when the selected outbound has changed.

Only inbound connections are affected by this setting, internal connections will always be interrupted.

### Filter Fields

See [Filter Fields](/configuration/shared/filter/) for details.
