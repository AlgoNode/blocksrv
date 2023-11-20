# Nodely BlkServer

This is an Algorand block (payset+evaldelta+cert) storage server. 

* Use Nodely Block exporter conduit plugin to import blocks with eval deltas into the server.
* Use Block server as virtual follower node or natively with Nodely BlkSrv importer conduit plugin.


# Example use 

```mermaid
flowchart LR
    NC[Light Relay] --> VF[Nodely Virtual Relay] 
    VF --> FO[Follower Node] 
    FO --> BE[Nodely EvalDelta Block\nExporter plugin]
    subgraph A[BlkSrv ETL]
        subgraph O[Optional]
            NC
            VF
        end
        FO
        subgraph C1[Conduit 1]
        BE
        end
    end
    A --> BS
    BS[Nodely EvalDelta\nBlock Server] -->|blocks| BI[Nodely BlkSrv\nimporter plugin] 
    BS <--> PD[(Compressed\nPebble DB)]
    BI --> OE[Online Stake\nExporter]
    OE --> CH[(ClickHouse\nstake history)] 
    subgraph Conduit 2
      BI
      OE
    end
```
