# Protocol Documentation
<a name="top"></a>

## Table of Contents

- [rmtaccessmgr/v1/rmtacessmgr.proto](#rmtaccessmgr_v1_rmtacessmgr-proto)
    - [AgentRemoteAccessSpec](#rmtaccessmgr-v1-AgentRemoteAccessSpec)
    - [ConfigError](#rmtaccessmgr-v1-ConfigError)
    - [GetRemoteAccessConfigByGuidRequest](#rmtaccessmgr-v1-GetRemoteAccessConfigByGuidRequest)
    - [GetResourceAccessConfigResponse](#rmtaccessmgr-v1-GetResourceAccessConfigResponse)
  
    - [ConfigStatus](#rmtaccessmgr-v1-ConfigStatus)
  
    - [RmtaccessmgrService](#rmtaccessmgr-v1-RmtaccessmgrService)
  
- [Scalar Value Types](#scalar-value-types)



<a name="rmtaccessmgr_v1_rmtacessmgr-proto"></a>
<p align="right"><a href="#top">Top</a></p>

## rmtaccessmgr/v1/rmtacessmgr.proto



<a name="rmtaccessmgr-v1-AgentRemoteAccessSpec"></a>

### AgentRemoteAccessSpec



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| remote_access_proxy_endpoint | [string](#string) |  |  |
| session_token | [string](#string) |  |  |
| reverse_bind_port | [uint32](#uint32) |  |  |
| target_host | [string](#string) |  |  |
| target_port | [uint32](#uint32) |  |  |
| ssh_user | [string](#string) |  |  |
| expiration_timestamp | [uint64](#uint64) |  |  |
| uuid | [string](#string) |  |  |






<a name="rmtaccessmgr-v1-ConfigError"></a>

### ConfigError



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| code | [string](#string) |  |  |






<a name="rmtaccessmgr-v1-GetRemoteAccessConfigByGuidRequest"></a>

### GetRemoteAccessConfigByGuidRequest



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| uuid | [string](#string) |  |  |
| tenantID | [string](#string) |  |  |






<a name="rmtaccessmgr-v1-GetResourceAccessConfigResponse"></a>

### GetResourceAccessConfigResponse



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| seq | [uint64](#uint64) |  |  |
| observed_at | [google.protobuf.Timestamp](#google-protobuf-Timestamp) |  |  |
| status | [ConfigStatus](#rmtaccessmgr-v1-ConfigStatus) |  |  |
| spec | [AgentRemoteAccessSpec](#rmtaccessmgr-v1-AgentRemoteAccessSpec) |  |  |
| error | [ConfigError](#rmtaccessmgr-v1-ConfigError) |  |  |





 


<a name="rmtaccessmgr-v1-ConfigStatus"></a>

### ConfigStatus


| Name | Number | Description |
| ---- | ------ | ----------- |
| CONFIG_STATUS_UNSPECIFIED | 0 |  |
| CONFIG_STATUS_NONE | 1 |  |
| CONFIG_STATUS_PENDING | 2 |  |
| CONFIG_STATUS_ACTIVE | 3 |  |
| CONFIG_STATUS_DISABLED | 4 |  |
| CONFIG_STATUS_ERROR | 5 |  |


 

 


<a name="rmtaccessmgr-v1-RmtaccessmgrService"></a>

### RmtaccessmgrService


| Method Name | Request Type | Response Type | Description |
| ----------- | ------------ | ------------- | ------------|
| GetRemoteAccessConfigByGuid | [GetRemoteAccessConfigByGuidRequest](#rmtaccessmgr-v1-GetRemoteAccessConfigByGuidRequest) | [GetResourceAccessConfigResponse](#rmtaccessmgr-v1-GetResourceAccessConfigResponse) |  |

 



## Scalar Value Types

| .proto Type | Notes | C++ | Java | Python | Go | C# | PHP | Ruby |
| ----------- | ----- | --- | ---- | ------ | -- | -- | --- | ---- |
| <a name="double" /> double |  | double | double | float | float64 | double | float | Float |
| <a name="float" /> float |  | float | float | float | float32 | float | float | Float |
| <a name="int32" /> int32 | Uses variable-length encoding. Inefficient for encoding negative numbers – if your field is likely to have negative values, use sint32 instead. | int32 | int | int | int32 | int | integer | Bignum or Fixnum (as required) |
| <a name="int64" /> int64 | Uses variable-length encoding. Inefficient for encoding negative numbers – if your field is likely to have negative values, use sint64 instead. | int64 | long | int/long | int64 | long | integer/string | Bignum |
| <a name="uint32" /> uint32 | Uses variable-length encoding. | uint32 | int | int/long | uint32 | uint | integer | Bignum or Fixnum (as required) |
| <a name="uint64" /> uint64 | Uses variable-length encoding. | uint64 | long | int/long | uint64 | ulong | integer/string | Bignum or Fixnum (as required) |
| <a name="sint32" /> sint32 | Uses variable-length encoding. Signed int value. These more efficiently encode negative numbers than regular int32s. | int32 | int | int | int32 | int | integer | Bignum or Fixnum (as required) |
| <a name="sint64" /> sint64 | Uses variable-length encoding. Signed int value. These more efficiently encode negative numbers than regular int64s. | int64 | long | int/long | int64 | long | integer/string | Bignum |
| <a name="fixed32" /> fixed32 | Always four bytes. More efficient than uint32 if values are often greater than 2^28. | uint32 | int | int | uint32 | uint | integer | Bignum or Fixnum (as required) |
| <a name="fixed64" /> fixed64 | Always eight bytes. More efficient than uint64 if values are often greater than 2^56. | uint64 | long | int/long | uint64 | ulong | integer/string | Bignum |
| <a name="sfixed32" /> sfixed32 | Always four bytes. | int32 | int | int | int32 | int | integer | Bignum or Fixnum (as required) |
| <a name="sfixed64" /> sfixed64 | Always eight bytes. | int64 | long | int/long | int64 | long | integer/string | Bignum |
| <a name="bool" /> bool |  | bool | boolean | boolean | bool | bool | boolean | TrueClass/FalseClass |
| <a name="string" /> string | A string must always contain UTF-8 encoded or 7-bit ASCII text. | string | String | str/unicode | string | string | string | String (UTF-8) |
| <a name="bytes" /> bytes | May contain any arbitrary sequence of bytes. | string | ByteString | str | []byte | ByteString | string | String (ASCII-8BIT) |

