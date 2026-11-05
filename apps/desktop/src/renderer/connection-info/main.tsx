// Shell page "connection-info" (docs/specs/desktop.md section 3.1, 4.6): the modal window with the addresses, the key and the snippets.
import { mountPage } from '../shared/mount';
import { ConnectionInfoPage } from './ConnectionInfoPage';

mountPage(<ConnectionInfoPage bridge={window.localaiDesktop} />);
