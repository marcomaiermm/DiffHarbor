export function Row({ id }: { id: string }) { return <li>{id}</li>; }
export function List({ ids }: { ids: string[] }) { return <ul>{ids.map((id) => <Row id={id} />)}</ul>; }
