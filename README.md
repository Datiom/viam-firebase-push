# Firebase Push Notification Module

A Viam module that provides a generic service to send push notifications via Firebase Cloud Messaging (FCM).

## Model datiom:viam-firebase-push:firebase-push

This model allows you to send push notifications to mobile devices using Firebase Cloud Messaging. FCM tokens can be configured in the service attributes and/or passed dynamically via doCommand.

### Requirements

A Firebase project must be set up, and a Firebase Admin SDK service account must be created.

### Configuration

The following attribute template can be used to configure this model:

```json
{
  "service_account_json": "<firebase service account JSON or object>",
  "fcm_tokens": ["<FCM token 1>", "<FCM token 2>", ...],
  "preset_messages": {
    "preset_name": {
      "title": "Notification Title",
      "body": "Notification Body"
    }
  },
  "enforce_preset": false
}
```

#### Attributes

The following attributes are available for this model:

| Name                   | Type             | Inclusion               | Description                                                                                                                                                                                |
|------------------------|------------------|-------------------------|--------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `service_account_json` | string or object | **Required if service_account_file not provided** | Firebase service account JSON credentials as either a JSON string or a JSON object                                                                                                         |
| `service_account_file` | string           | **Required if service_account_json not provided** | Path to Firebase service account credentials JSON file                                                                                                                               |
| `fcm_tokens`           | array or string  | Optional                | Default list of FCM tokens to send notifications to. Can be a single string or an array of strings. If not specified, tokens must be provided in the doCommand call.                        |
| `preset_messages`      | object           | Optional                | An object with key (preset name) and value (object with title and body) pairs that can be used to send pre-configured messages. Template strings can be embedded within double angle brackets, for example: <<to_replace>> |
| `enforce_preset`       | boolean          | Optional, default false | If set to true, preset_messages must be configured and a preset message must be selected when sending a notification.                                                                       |

#### Example Configuration

With a JSON string for service_account_json:

```json
{
  "service_account_json": "{\"type\":\"service_account\",\"project_id\":\"your-project-id\",\"private_key_id\":\"your-private-key-id\",\"private_key\":\"-----BEGIN PRIVATE KEY-----\\nYour Private Key\\n-----END PRIVATE KEY-----\\n\",\"client_email\":\"firebase-adminsdk-xxxxx@your-project-id.iam.gserviceaccount.com\",\"client_id\":\"your-client-id\",\"auth_uri\":\"https://accounts.google.com/o/oauth2/auth\",\"token_uri\":\"https://oauth2.googleapis.com/token\",\"auth_provider_x509_cert_url\":\"https://www.googleapis.com/oauth2/v1/certs\",\"client_x509_cert_url\":\"https://www.googleapis.com/robot/v1/metadata/x509/firebase-adminsdk-xxxxx%40your-project-id.iam.gserviceaccount.com\",\"universe_domain\":\"googleapis.com\"}",
  "fcm_tokens": ["FCM_TOKEN_1", "FCM_TOKEN_2"],
  "preset_messages": {
    "alert": {
      "title": "Alert: <<alert_type>>",
      "body": "This is an alert about <<alert_details>>."
    },
    "welcome": {
      "title": "Welcome!",
      "body": "Welcome to our service!"
    }
  },
  "enforce_preset": true
}
```

With a JSON object for service_account_json:

```json
{
  "service_account_json": {
    "type": "service_account",
    "project_id": "your-project-id",
    "private_key_id": "your-private-key-id",
    "private_key": "-----BEGIN PRIVATE KEY-----\nYour Private Key\n-----END PRIVATE KEY-----\n",
    "client_email": "firebase-adminsdk-xxxxx@your-project-id.iam.gserviceaccount.com",
    "client_id": "your-client-id",
    "auth_uri": "https://accounts.google.com/o/oauth2/auth",
    "token_uri": "https://oauth2.googleapis.com/token",
    "auth_provider_x509_cert_url": "https://www.googleapis.com/oauth2/v1/certs",
    "client_x509_cert_url": "https://www.googleapis.com/robot/v1/metadata/x509/firebase-adminsdk-xxxxx%40your-project-id.iam.gserviceaccount.com",
    "universe_domain": "googleapis.com"
  },
  "fcm_tokens": ["FCM_TOKEN_1", "FCM_TOKEN_2"],
  "preset_messages": {
    "alert": {
      "title": "Alert: <<alert_type>>",
      "body": "This is an alert about <<alert_details>>."
    },
    "welcome": {
      "title": "Welcome!",
      "body": "Welcome to our service!"
    }
  },
  "enforce_preset": true
}
```

### DoCommand

This service implements `DoCommand` with the following commands:

#### send

Send a push notification to FCM tokens.

##### Parameters

| Name              | Type             | Inclusion                                | Description                                                         |
|-------------------|------------------|------------------------------------------|---------------------------------------------------------------------|
| `title`           | string           | **Required if preset not used**          | The notification title                                              |
| `body`            | string           | **Required if preset not used**          | The notification body                                               |
| `data`            | object           | Optional                                 | Additional data to send with the notification                        |
| `preset`          | string           | **Required if enforce_preset is true**   | The name of a preset message to use                                 |
| `template_vars`   | object           | Optional                                 | Variables to replace in preset message templates                     |
| `fcm_tokens`      | array or string  | **Required if not configured**           | FCM tokens to send the notification to. Overrides configured tokens if provided. |

##### Example DoCommand

Basic notification to configured tokens:

```json
{
  "command": "send",
  "title": "Hello",
  "body": "This is a test notification"
}
```

With custom FCM tokens:

```json
{
  "command": "send",
  "title": "Hello",
  "body": "This is a test notification",
  "fcm_tokens": ["CUSTOM_FCM_TOKEN_1", "CUSTOM_FCM_TOKEN_2"]
}
```

Using a preset message with template variables:

```json
{
  "command": "send",
  "preset": "alert",
  "template_vars": {
    "alert_type": "Motion Detected",
    "alert_details": "Movement detected in the kitchen"
  },
  "fcm_tokens": "SINGLE_FCM_TOKEN"
}
```

##### Response

```json
{
  "success": true,
  "sent_count": 2,
  "failed_count": 0,
  "sent_to_tokens": 2
}
```
